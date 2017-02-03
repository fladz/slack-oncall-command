package slackoncallbot

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/schema"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// func init {{{

func init() {
	// Parse Env from app.yaml config.
	loadConfiguration()

	// Prepare generic error and help text.
	setErrorText()
	setHelpText()

	// Prepare rotation struct
	rotations = make(oncallProperties, 0)

	// Prepare user structs
	slackUsers = make(map[string]*slackUser, 0)

	// Start request handler.
	http.HandleFunc("/", oncallHandler)
} // }}}

// func oncallHandler {{{

// Initial HTTP handler.
//
// Extract request from Slack and do various pre-sanity checks on request then
// dispatch requests to a proper operation handler.
func oncallHandler(w http.ResponseWriter, r *http.Request) {
	var err error

	// Create a request context
	ctx := appengine.NewContext(r)
	// Set timeout for this request so we won't keep the requestor waiting for ever.
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, opTimeout)
	defer cancel()

	if err = r.ParseForm(); err != nil {
		log.Warningf(ctx, "error parsing request params from slack: %v", err)
		fmt.Fprintf(w, errorExternal)
		return
	}
	defer r.Body.Close()

	// Decode the request params into our request struct.
	var sr slackCommandParams
	dec := schema.NewDecoder()
	if err = dec.Decode(&sr, r.Form); err != nil {
		log.Warningf(ctx, "error decoding request params: %s", err)
		fmt.Fprintf(w, errorExternal)
		return
	}

	// Make sure the token we received is what we expect.
	if sr.Token != slackCommandToken {
		log.Warningf(ctx, "invalid token %s", sr.Token)
		fmt.Fprintf(w, errorExternal)
		return
	}

	// Make sure the requested command is what we support.
	if sr.Command != command {
		log.Warningf(ctx, "unknown command %s, supported command - %s", sr.Command, command)
		fmt.Fprintf(w, errorExternal)
		return
	}

	// Decode parameters passed.
	operation, params, errstr := decodeOperationParams(ctx, sr)
	if errstr != "" {
		switch errstr {
		case errorInput:
			// In case of input errors, display help text for the operation
			// they tried to run.
			w.Write([]byte(help(ctx, operation)))
		default:
			// Anything else, print out the error string itself.
			w.Write([]byte(errstr))
		}
		return
	}

	// If this is the first time called, get the current list of oncall rotation first.
	if len(rotations) == 0 {
		if err = loadState(ctx); err != nil {
			log.Warningf(ctx, "error loading oncall state - %s", err)
			w.Write([]byte(errorExternal))
			return
		}
	}

	var res slackResponse
	switch operation {
	case "list": // List current oncall rotations.
		res = list(ctx, params)
	case "add": // Add a user in rotation.
		res = add(ctx, params)
	case "flush": // Flush a current rotation.
		res = flush(ctx, params)
	case "remove": // Remove a user from rotation.
		res = remove(ctx, params)
	case "swap": // Swap 2 positions in a rotation.
		res = swap(ctx, params)
	case "register": // Add a new team to manage oncall list for.
		res = register(ctx, params)
	case "unregister": // Remove a manager from a team.
		res = unregister(ctx, params)
	case "update":
		res = update(ctx, params)
	default: // Dump available operations and params.
		w.Write([]byte(help(ctx, "")))
		return
	}

	// Ok let's send it!
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(res); err != nil {
		w.Write([]byte(errorExternal))
	}
} // }}}

// func help {{{

// help
//
// Display available operations and usage.
// This will be called when "help" operation is issued, no/unknown operation is issued,
// or any of user input is invalid. (ie. missing parameters)
func help(ctx context.Context, scope string) string {
	str := "Usage:\n"
	switch scope {
	case "list":
		str += helpList
	case "add":
		str += helpAdd
	case "remove":
		str += helpRemove
	case "swap":
		str += helpSwap
	case "flush":
		str += helpFlush
	case "register":
		str += helpRegister
	case "unregister":
		str += helpUnregister
	case "update":
		str += helpUpdate
	default:
		str += strings.Join([]string{helpList, helpAdd, helpRemove, helpSwap, helpFlush, helpRegister, helpUnregister, helpUpdate}, "\n")
	}
	return str
} // }}}

// func list {{{

// list {team}
//
// If "team" parameter is given, display current oncall rotation of the team.
// If the parmeter is null, display ops manager of each team the oncall bot manages.
func list(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opList)
	if !ok {
		return slackResponse{Text: help(ctx, "list")}
	}
	if p.team == "" {
		// Display list of manager(s)/team.
		return listTeams(ctx)
	}
	return listRotation(ctx, p.team)
} // }}}

// func add {{{

// add {team} {@slack_username} {label}
//
// Add the user in the team's rotation.
// "label" is optional, this could be used to identify the user's "area of responsibility" if a team
// has multiple different areas.
//
// Example usage for the "label" -
// Set primary staff "system", secondary "developer", teritary "support" in "label" parameter.
// It would set oncall list as -
//  1: @tech-staff1 123-4567-8900 (system)
//  2: @tech-staff2 111-1111-1111 (developer)
//  3: @non-tech-staff 222-222-2222 (support)
//
// The person who will contact this team doesn't need to care exactly where the problem resides, the primary staff
// in the team can then relay the info to proper person.
// Or if the person already knows it's an application issue then (s)he can contact secondary staff directly
// as the primary staff is not developer.
func add(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opAdd)
	if !ok || p.team == "" || p.name == "" || p.id == "" {
		return slackResponse{Text: help(ctx, "add")}
	}

	res := slackResponse{}
	// Make sure the requested staff exists.
	u, err := getSlackUserDetail(ctx, p.id, false)
	if err != nil {
		log.Warningf(ctx, "(add) error getting user %s - %s", p.name, err)
		res.Text = errorExternal
		return res
	}
	if u == nil {
		res.Text = fmt.Sprintf("<@%s> doesn't exist in Slack %s", p.name, humanErrorEmoji)
		return res
	}

	// Get list of current oncall for this team first.
	current := getCurrentRotation(p.team)
	if current == nil {
		res.Text = fmt.Sprintf("Team %s is not registered in oncall command! %s", p.team, humanErrorEmoji)
		return res
	}

	// Ok now let's check if the requested staff is already in rotation or not.
	var updated time.Time
	var updatedBy string
	oncallMut.Lock()
	if len(current.Rotations) == 0 {
		// Add and save.
		current.Rotations = append(current.Rotations, RotationProperty{Name: p.name, Id: p.id, Label: p.label})
		updated = current.Updated
		updatedBy = current.UpdatedBy
		current.Updated = time.Now()
		current.UpdatedBy = p.by.name
		if err = saveState(ctx, current); err != nil {
			log.Warningf(ctx, "(add) error saving state - %s", err)
			// Revert the changes.
			current.Rotations = nil
			current.Updated = updated
			current.UpdatedBy = updatedBy
			res.Text = errorExternal
			oncallMut.Unlock()
			return res
		}
		res.Text = fmt.Sprintf("Success! <@%s> added to the on-call list for %s\nNew list:", p.name, p.team)
		oncallMut.Unlock()
		res.Attachments = []attachment{generateOncallList(ctx, p.team)}
		return res
	}

	// This team already has a rotation, let's check.
	var currentName, currentLabel string
	for i := 0; i < len(current.Rotations); i++ {
		// Make sure there is no dupe.
		if current.Rotations[i].Id == p.id {
			// If there's a dupe, possibly the name and/or label was changed.
			if p.name == current.Rotations[i].Name && p.label == current.Rotations[i].Label {
				res.Text = fmt.Sprintf("<@%s> already assigned %s rotation %s", p.name, p.team, humanErrorEmoji)
				oncallMut.Unlock()
				return res
			}
			currentName = current.Rotations[i].Name
			currentLabel = current.Rotations[i].Label
			// Same user, different name or label. In this case we ignore the position. We'll just update the diffs.
			updated = current.Updated
			updatedBy = current.UpdatedBy
			current.Rotations[i].Name = p.name
			current.Rotations[i].Label = p.label
			current.Updated = time.Now()
			current.UpdatedBy = p.by.name
			if err := saveState(ctx, current); err != nil {
				log.Warningf(ctx, "(add) error saving state - %s", err)
				current.Rotations[i].Name = currentName
				current.Rotations[i].Label = currentLabel
				current.Updated = updated
				current.UpdatedBy = updatedBy
				res.Text = errorExternal
				oncallMut.Unlock()
				return res
			}
			res.Text = fmt.Sprintf("Success! Information updated for <@%s>\nNew list:", p.name)
			oncallMut.Unlock()
			res.Attachments = []attachment{generateOncallList(ctx, p.team)}
			return res
		}
	}

	// Ok, the user doesn't exist in rotation. Let's append.
	updated = current.Updated
	updatedBy = current.UpdatedBy
	current.Rotations = append(current.Rotations, RotationProperty{Name: p.name, Id: p.id, Label: p.label})
	current.Updated = time.Now()
	current.UpdatedBy = p.by.name
	if err = saveState(ctx, current); err != nil {
		log.Warningf(ctx, "(add) error saving state - %s", err)
		current.Rotations = current.Rotations[:(len(current.Rotations) - 1)]
		current.Updated = updated
		current.UpdatedBy = updatedBy
		res.Text = errorExternal
		oncallMut.Unlock()
		return res
	}

	res.Text = fmt.Sprintf("Success! <@%s> added to the on-call list for %s\nNew list:", p.name, p.team)
	oncallMut.Unlock()
	res.Attachments = []attachment{generateOncallList(ctx, p.team)}
	return res
} // }}}

// func flush {{{

// flush {team}
//
// Flush current oncall rotation from the team.
func flush(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opFlush)
	if !ok || p.team == "" {
		return slackResponse{Text: help(ctx, "flush")}
	}

	res := slackResponse{}

	// Get current oncall rotation for this team.
	current := getCurrentRotation(p.team)
	if current == nil {
		res.Text = fmt.Sprintf("Sorry, team %s does not exist %s", p.team, humanErrorEmoji)
		return res
	}

	// Backup current rotation in case the update fails.
	oncallMut.Lock()
	defer oncallMut.Unlock()
	r := current.Rotations
	updated := current.Updated
	updatedBy := current.UpdatedBy
	current.Rotations = nil
	current.Updated = time.Now()
	current.UpdatedBy = p.by.name
	if err := saveState(ctx, current); err != nil {
		log.Warningf(ctx, "(flush) error saving state - %s", err)
		current.Rotations = r
		current.Updated = updated
		current.UpdatedBy = updatedBy
		res.Text = errorExternal
		return res
	}

	res.Text = fmt.Sprintf("Success! Removed all on-call list from %s", p.team)
	return res
} // }}}

// func remove {{{

// remove {team} {@slack_username}
//
// Remove the user from the team's rotation.
func remove(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opRemove)
	if !ok || p.team == "" || p.name == "" || p.id == "" {
		return slackResponse{Text: help(ctx, "remove")}
	}

	res := slackResponse{}
	// Get the current rotation for this team.
	current := getCurrentRotation(p.team)
	if current == nil {
		res.Text = fmt.Sprintf("Team %s is not registered in oncall command %s", p.team, humanErrorEmoji)
		return res
	}

	// Check if we have this staff in rotation.
	oncallMut.Lock()
	if len(current.Rotations) == 0 {
		res.Text = fmt.Sprintf("Team %s doesn't have anyone in list %s", p.team, humanErrorEmoji)
		oncallMut.Unlock()
		return res
	}
	updated := current.Updated
	updatedBy := current.UpdatedBy
	r := current.Rotations
	// Find the staff requested for removal.
	for i := 0; i < len(current.Rotations); i++ {
		if current.Rotations[i].Id == p.id {
			// This is the requested user to be removed.
			current.Rotations = append(current.Rotations[:i], current.Rotations[i+1:]...)
			current.Updated = time.Now()
			current.UpdatedBy = p.by.name
			if err := saveState(ctx, current); err != nil {
				log.Warningf(ctx, "(remove) error saving state - %s", err)
				current.Rotations = r
				current.Updated = updated
				current.UpdatedBy = updatedBy
				res.Text = errorExternal
				oncallMut.Unlock()
				return res
			}
			res.Text = fmt.Sprintf("Success! <@%s> removed from the on-call list for %s\nNew list:", p.name, p.team)
			oncallMut.Unlock()
			res.Attachments = []attachment{generateOncallList(ctx, p.team)}
			return res
		}
	}

	oncallMut.Unlock()
	res.Text = fmt.Sprintf("Sorry, <@%s> is not in the on-call list for %s %s", p.name, p.team, humanErrorEmoji)
	return res
} // }}}

// func swap {{{

// swap {team} {position_A} {position_B}
//
// Swap position_A rotation and position_B rotation of the {team}.
func swap(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opSwap)
	if !ok || p.team == "" || len(p.positions) != 2 {
		return slackResponse{Text: help(ctx, "swap")}
	}

	res := slackResponse{}
	// If given position_A and position_B are same, nothing to do.
	if p.positions[0] == p.positions[1] {
		res.Text = "position_A and position_B are same, nothing to do!"
		return res
	}

	// Get the current rotation of the team.
	current := getCurrentRotation(p.team)
	if current == nil {
		res.Text = fmt.Sprintf("Sorry, team %s does not exist %s", p.team, humanErrorEmoji)
		return res
	}

	// If there's less than 2 staff in rotation, we cannot swap.
	oncallMut.Lock()
	rlen := len(current.Rotations)
	if rlen < 2 || rlen < p.positions[0] || rlen < p.positions[1] {
		res.Text = fmt.Sprintf("Sorry, swap could not be completed! Check _position_a_ and _position_b_ %s", humanErrorEmoji)
		oncallMut.Unlock()
		return res
	}

	// Copy over current rotation first.
	currentRotation := current.Rotations
	currentUpdated := current.Updated
	currentUpdatedBy := current.UpdatedBy

	// Swap and save the new rotation in state.
	current.Rotations[p.positions[0]-1], current.Rotations[p.positions[1]-1] =
		current.Rotations[p.positions[1]-1], current.Rotations[p.positions[0]-1]
	current.Updated = time.Now()
	current.UpdatedBy = p.by.name
	if err := saveState(ctx, current); err != nil {
		log.Warningf(ctx, "(swap) error saving state - %s", err)
		// Replace the rotation list
		current.Rotations = currentRotation
		current.Updated = currentUpdated
		current.UpdatedBy = currentUpdatedBy
		res.Text = errorExternal
		oncallMut.Unlock()
		return res
	}

	res.Text = fmt.Sprintf("Success! Swapped position %d and %d in the on-call list for %s\nNew list:", p.positions[0], p.positions[1], p.team)
	oncallMut.Unlock()
	res.Attachments = []attachment{generateOncallList(ctx, p.team)}
	return res
} // }}}

// func register {{{

// register {team} {@slack_username}
//
// Register a new team to be mamaged by this oncall process.
// If "@slack_username" is defined, set the person as the team manager.
// This operation can also be used to assign an additional manager to existing team.
func register(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opRegister)
	if !ok || p.team == "" || (p.name != "" && p.id == "") {
		return slackResponse{Text: help(ctx, "register")}
	}

	res := slackResponse{}
	// If the manager is provided, make sure the person exists.
	if p.name != "" {
		u, err := getSlackUserDetail(ctx, p.id, false)
		if err != nil {
			log.Warningf(ctx, "(register) error getting user %s - %s", p.name, err)
			res.Text = errorExternal
			return res
		}
		if u == nil {
			res.Text = fmt.Sprintf("<@%s> doesn't exist in Slack %s", externalErrorEmoji)
			return res
		}
	}

	// Check if the team already exists.
	r := getCurrentRotation(p.team)
	if r == nil {
		r = &oncallProperty{Team: p.team, Managers: make([]ManagerProperty, 0)}
		if p.name != "" {
			r.Managers = append(r.Managers, ManagerProperty{Name: p.name, Id: p.id})
		}
		r.Updated = time.Now()
		r.UpdatedBy = p.by.name
		// Save the state first.
		if err := saveState(ctx, r); err != nil {
			log.Warningf(ctx, "(register) error saving state - %s", err)
			res.Text = errorExternal
			return res
		}
		// Saved in external storage, let's save in memory now.
		oncallMut.Lock()
		rotations = append(rotations, r)
		sort.Sort(rotations)
		oncallMut.Unlock()
		if p.name == "" {
			res.Text = fmt.Sprintf("Success! New team %s registered", p.team)
			return res
		} else {
			res.Text = fmt.Sprintf("Success! New team %s registered, with manager <@%s>", p.team, p.name)
			return res
		}
	}

	// The row already exists, do we need to add this manager?
	if p.name == "" {
		res.Text = fmt.Sprintf("Team %s has already been registered %s", p.team, humanErrorEmoji)
		return res
	}

	// Let's check and add the manager now.
	oncallMut.Lock()
	defer oncallMut.Unlock()
	for _, m := range r.Managers {
		if m.Id == p.id {
			res.Text = fmt.Sprintf("Team %s, manager <@%s> has already been registered %s", p.team, p.name, humanErrorEmoji)
			return res
		}
	}
	currentTime := r.Updated
	currentRequestor := r.UpdatedBy
	r.Managers = append(r.Managers, ManagerProperty{Name: p.name, Id: p.id})
	r.Updated = time.Now()
	r.UpdatedBy = p.by.name
	if err := saveState(ctx, r); err != nil {
		log.Warningf(ctx, "(register) error saving state - %s", err)
		// Failed saving in storage, revert the change so next time this will again be a new change.
		r.Updated = currentTime
		r.UpdatedBy = currentRequestor
		r.Managers = r.Managers[:(len(r.Managers) - 1)]
		res.Text = errorExternal
		return res
	}
	res.Text = fmt.Sprintf("Success! <@%s> added as a manager of team %s", p.name, p.team)
	return res
} // }}}

// func unregister {{{

// unregister {team} {@slack_username}
//
// If @slack_username is defined, remove the manager from the team.
// If @slack_username is not defined, remove the team from managed team list.
func unregister(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opUnregister)
	if !ok || p.team == "" || (p.name != "" && p.id == "") {
		return slackResponse{Text: help(ctx, "unregister")}
	}

	res := slackResponse{}
	// Let's check if we have this team.
	r := getCurrentRotation(p.team)
	if r == nil {
		res.Text = fmt.Sprintf("Team %s is not registered in oncall command %s:", p.team, humanErrorEmoji)
		return res
	}

	// If manager parameter value is not defined, delete the team itself.
	oncallMut.Lock()
	defer oncallMut.Unlock()
	if p.name == "" {
		for i := 0; i < len(rotations); i++ {
			if rotations[i].Team == p.team {
				// This is the one to remove, delete from state first.
				if err := deleteState(ctx, rotations[i].Key); err != nil {
					log.Warningf(ctx, "(unregister) error deleting state - %s", err)
					res.Text = errorExternal
					return res
				}
				// Deleted from state, let's delete from memory and return.
				rotations = append(rotations[:i], rotations[i+1:]...)
				res.Text = fmt.Sprintf("Success! Team %s removed from oncall command", p.team)
				return res
			}
		}
		res.Text = fmt.Sprintf("Team %s already unregistered from oncall command %s", p.team, humanErrorEmoji)
		return res
	}

	// Let's check if we have this manager.
	for i := 0; i < len(r.Managers); i++ {
		if r.Managers[i].Id == p.id {
			// Demote this person.
			r.Managers = append(r.Managers[:i], r.Managers[i+1:]...)
			updated := r.Updated
			updatedBy := r.UpdatedBy
			r.Updated = time.Now()
			r.UpdatedBy = p.by.name
			if err := saveState(ctx, r); err != nil {
				log.Warningf(ctx, "(unregister) error saving state - %s", err)
				// Failed saving the state, revert changes.
				r.Managers = append(r.Managers, ManagerProperty{Name: p.name, Id: p.id})
				r.Updated = updated
				r.UpdatedBy = updatedBy
				res.Text = errorExternal
				return res
			}
			res.Text = fmt.Sprintf("Success! Manager <@%s> removed as a manager from team %s", p.name, p.team)
			return res
		}
	}

	res.Text = fmt.Sprintf("Sorry, <@%s> is not a manager of team %s %s", p.name, p.team, humanErrorEmoji)
	return res
} // }}}

// func update {{{

// update
//
// Update the requstor's Slack user profile cache.
func update(ctx context.Context, params interface{}) slackResponse {
	p, ok := params.(opUpdate)
	if !ok || p.id == "" || p.name == "" {
		return slackResponse{Text: help(ctx, "update")}
	}
	u, err := getSlackUserDetail(ctx, p.id, true)
	if err != nil {
		log.Warningf(ctx, "(update) error getting user info %s - %s", p.name, err)
		return slackResponse{Text: errorExternal}
	}
	if u == nil {
		return slackResponse{Text: fmt.Sprintf("Sorry! You don't exist in Slack %s", humanErrorEmoji)}
	}
	return slackResponse{Text: "Success! Your information is now up to date!"}
} // }}}

// func listTeams {{{

// Display manager(s) of each team the command manages.
func listTeams(ctx context.Context) slackResponse {
	var user *slackUser
	var err error

	res := slackResponse{Text: "List of Teams and Managers:", Attachments: make([]attachment, 1)}
	att := attachment{Color: defaultColor}
	var str []string
	oncallMut.RLock()
	for _, r := range rotations {
		if len(r.Managers) == 0 {
			str = append(str, fmt.Sprintf("%s: %s", r.Team, errorNoManager))
			continue
		}
		for _, manager := range r.Managers {
			// Get user info.
			if user, err = getSlackUserDetail(ctx, manager.Id, false); err != nil || user == nil || user.phone == "" {
				str = append(str, fmt.Sprintf("%s: <@%s> %s", r.Team, manager.Name, errorNoPhone))
			} else {
				str = append(str, fmt.Sprintf("%s: <@%s> %s", strings.ToUpper(r.Team), manager.Name, user.phone))
			}
		}
	}
	oncallMut.RUnlock()

	att.Text = strings.Join(str, "\n")
	res.Attachments[0] = att
	return res
} // }}}

// func listRotation {{{

// Display oncall rotation of the team.
//
// We'll display -
// {TEAM} Manager {slackusername}
//   {position} {slackusername} {phone} {label}
//   {position} {slackusername} {phone} {label}
//   ...
func listRotation(ctx context.Context, team string) slackResponse {
	return slackResponse{Text: "On-call list for: " + team, Attachments: []attachment{generateOncallList(ctx, team)}}
} // }}}

// func generateOncallList {{{

// Return on-call list along with list of managers for the requested team.
func generateOncallList(ctx context.Context, team string) attachment {
	var row *oncallProperty
	var err error
	att := attachment{Color: defaultColor}

	// Get current list.
	oncallMut.RLock()
	for _, r := range rotations {
		if r.Team == team {
			row = r
			break
		}
	}
	if row == nil {
		// No rotation!
		att.Text = fmt.Sprintf("Team %s does not exist %s", team, humanErrorEmoji)
		oncallMut.RUnlock()
		return att
	}
	att.Footer = fmt.Sprintf("updated: %s by <@%s>", row.Updated.In(timezone).Format(dateFormat), row.UpdatedBy)

	// Copy over current oncall list in case any of managers or on-call staff is deleted from Slack
	// and needs to be removed from on-call as well.
	var newOncallList = oncallProperty{
		Key:       row.Key,
		Team:      row.Team,
		Managers:  row.Managers,
		Rotations: row.Rotations,
		Updated:   row.Updated,
		UpdatedBy: row.UpdatedBy,
	}
	oncallMut.RUnlock()

	// Get list of managers.
	var changed bool
	tmp, str := getCurrentManagerOncallList(ctx, &newOncallList)
	if str == nil {
		att.Title = errorNoManager
	} else {
		att.Title = strings.Join(str, "\n")
	}
	if tmp {
		changed = tmp
	}

	// Then the actual list.
	tmp, str = getCurrentOncallList(ctx, &newOncallList)
	if str == nil {
		att.Text = errorNoRotation
	} else {
		att.Text = strings.Join(str, "\n")
	}
	if tmp {
		changed = tmp
	}

	// If the list changed, update state and memory.
	if changed {
		if err = saveState(ctx, &newOncallList); err == nil {
			oncallMut.Lock()
			log.Infof(ctx, "updated manager list (%s) len %d->%d", team, len(row.Managers), len(newOncallList.Managers))
			row.Managers = newOncallList.Managers
			log.Infof(ctx, "updated on-call list (%s) len %d->%d", team, len(row.Rotations), len(newOncallList.Rotations))
			row.Rotations = newOncallList.Rotations
			oncallMut.Unlock()
		}
	}

	return att
} // }}}

// func getCurrentManagerOncallList {{{

func getCurrentManagerOncallList(ctx context.Context, row *oncallProperty) (changed bool, str []string) {
	if len(row.Managers) == 0 {
		return
	}

	for idx, m := range row.Managers {
		// Get info first.
		user, err := getSlackUserDetail(ctx, m.Id, false)
		if err == nil && user == nil {
			// User doesn't exist in Slack, remove from list.
			row.Managers = append(row.Managers[:idx], row.Managers[idx+1:]...)
			changed = true
			idx--
		} else {
			if err != nil || user.phone == "" {
				str = append(str, fmt.Sprintf("Manager: <@%s> %s", m.Name, errorNoPhone))
			} else {
				str = append(str, fmt.Sprintf("Manager: <@%s> %s", m.Name, user.phone))
			}
		}
	}

	return
} // }}}

// func getCurrentOncallList {{{

func getCurrentOncallList(ctx context.Context, row *oncallProperty) (changed bool, str []string) {
	if len(row.Rotations) == 0 {
		return
	}

	for idx, u := range row.Rotations {
		user, err := getSlackUserDetail(ctx, u.Id, false)
		var userstr string
		if err == nil && user == nil {
			// User doesn't exist in Slack, remove from list.
			row.Rotations = append(row.Rotations[:idx], row.Rotations[idx+1:]...)
			changed = true
			idx--
		} else {
			userstr = fmt.Sprintf("%d: <@%s> ", idx+1, u.Name)
			if err != nil || user.phone == "" {
				userstr += errorNoPhone
			} else {
				userstr += user.phone
			}
			if u.Label != "" {
				userstr += fmt.Sprintf(" (%s)", u.Label)
			}
			str = append(str, userstr)
		}
	}

	return
} // }}}
