package slackoncallbot

import (
	"fmt"
	"golang.org/x/net/context"
	"google.golang.org/appengine/log"
	"os"
	"strconv"
	"strings"
	"time"
)

// func loadConfiguration {{{

// Get configured values from ENV variables.
func loadConfiguration() {
	var err error
	var tmp string
	slackCommandToken = os.Getenv("slack_command_token")
	slackAPIToken = os.Getenv("slack_api_token")
	// Update command endpoint if defined.
	if tmp = os.Getenv("command_endpoint"); tmp != "" {
		command = tmp
	}
	// Update per-operation timeout if defined.
	if tmp = os.Getenv("operation_timeout"); tmp == "" {
		tmp = "3s"
	}
	if opTimeout, err = time.ParseDuration(tmp); err != nil {
		// Invalid timeout, use default.
		opTimeout = time.Duration(3 * time.Second)
	}
	// Update user cache timeout if defined.
	if tmp = os.Getenv("user_cache_timeout"); tmp == "" {
		tmp = "1d"
	}
	if cacheTimeout, err = time.ParseDuration(tmp); err != nil {
		// Invalid timeout, use default.
		cacheTimeout = time.Duration(24 * time.Hour)
	}
	// Update timezone to use if defined.
	tmp = os.Getenv("timezone")
	if timezone, err = time.LoadLocation(tmp); err != nil {
		// Invalid timezone, use default.
		timezone, _ = time.LoadLocation("UTC")
	}
	// Get list of superusers if configured
	if tmp = os.Getenv("superusers"); tmp != "" {
		superusers = strings.Split(tmp, ",")
	}
	// Check if we need to allow Slack users to be superusers.
	if tmp = os.Getenv("demote_admins"); strings.ToLower(tmp) == "true" {
		// We need someone to be a superuser, so unless the "superusers" option is already set,
		// we cannot disable admin permissions.
		if len(superusers) > 0 {
			adminDisabled = true
		}
	}
	// Generate "@admins" default Slack admin ID.
	if tmp = os.Getenv("admin_sub_team_id"); tmp != "" {
		adminFullName = "<!subteam^" + tmp + "|@admin>"
	} else {
		adminFullName = "@admin"
	}
	// For fun - use custom emoji's if configured.
	if tmp = os.Getenv("input_error_emoji"); tmp != "" {
		humanErrorEmoji = tmp
	}
	if tmp = os.Getenv("external_error_emoji"); tmp != "" {
		externalErrorEmoji = tmp
	}
} // }}}

// func setErrorText {{{

// Prepare static error text for generic errors.
func setErrorText() {
	errorInput = fmt.Sprintf("Invalid input %s", humanErrorEmoji)
	errorNoPerm = fmt.Sprintf("Sorry! you can't do that %s", humanErrorEmoji)
	errorExternal = fmt.Sprintf("Unexpected error occurred, please contact %s %s", adminFullName, externalErrorEmoji)
	errorNoRotation = fmt.Sprintf("On-call list not set %s", humanErrorEmoji)
	errorNoManager = fmt.Sprintf("Manager not set %s", humanErrorEmoji)
	errorNoPhone = fmt.Sprintf("Phone not set %s", humanErrorEmoji)
} // }}}

// func setHelpText {{{

// Create static help text for each operation.
func setHelpText() {
	helpList = fmt.Sprintf("`%s list`\n\tDisplay list of teams and their managers\n`%s list {team}`\n\tDisplay on-call list for _team_", command, command)
	helpAdd = fmt.Sprintf("`%s add {team} {@slackusername} {label}`\n\tAdd _@slackusername_ to on-call list for _team_ with optional _label_", command)
	helpFlush = fmt.Sprintf("`%s flush {team}`\n\tFlush the entire on-call list for _team_", command)
	helpRemove = fmt.Sprintf("`%s remove {team} {@slackusername}`\n\tRemove _@slackusername_ from on-call list for _team_", command)
	helpSwap = fmt.Sprintf("`%s swap {team} {position_a} {position_b}`\n\tSwap _position_a_ and _position_b_ in the on-call list for _team_", command)
	helpRegister = fmt.Sprintf("`%s register {team} {@slackusername}`\n\tRegister a new _team_ with _@slackusername_ as it's manager", command)
	helpUnregister = fmt.Sprintf("`%s unregister {team} {@slackusername}`\n\tUnregister _team_ from oncall command, or remove _@slackusername_ from _team_ manager list", command)
	helpUpdate = fmt.Sprintf("`%s update`\n\tUpdate your Slack profile", command)
} // }}}

// func decodeOperationParams {{{

// Retrieve operation and provided parameter values for the operation from "text" value
// in the original Slack request body.
func decodeOperationParams(ctx context.Context, params slackCommandParams) (string, interface{}, string) {
	stuff := strings.Split(params.Text, " ")
	if len(stuff) == 0 {
		return "", nil, errorInput
	}
	req := opRequestor{name: params.UserName, id: params.UserId}

	var op = strings.ToLower(stuff[0])
	switch op {
	case "list":
		return decodeListParams(ctx, stuff)
	case "add":
		return decodeAddParams(ctx, req, stuff)
	case "remove":
		return decodeRemoveParams(ctx, req, stuff)
	case "swap":
		return decodeSwapParams(ctx, req, stuff)
	case "flush":
		return decodeFlushParams(ctx, req, stuff)
	case "register":
		return decodeRegisterParams(ctx, req, stuff)
	case "unregister":
		return decodeUnregisterParams(ctx, req, stuff)
	case "update":
		return decodeUpdateParams(ctx, req)
	}

	// Anything else including unsupported operations, just return help text.
	return "help", nil, ""
} // }}}

// func decodeListParams {{{

// list {team}
//   team - optional
func decodeListParams(ctx context.Context, stuff []string) (string, interface{}, string) {
	op := "list"
	if len(stuff) == 1 {
		return op, opList{}, ""
	}
	if len(stuff) != 2 {
		log.Warningf(ctx, "(%s) invalid input - %v", op, stuff)
		return op, opList{}, errorInput
	}
	return op, opList{team: strings.ToUpper(stuff[1])}, ""
} // }}}

// func decodeAddParams {{{

// add {team} {@slackusername} {label}
//   team  - required
//   name  - required
//   label - optional
//
// This operation requires manager of the team or superuser permission.
func decodeAddParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "add"
	if len(stuff) < 3 || len(stuff) > 4 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	// Decode user_id/user_name string from Slack into id and name.
	id, name := decodeUserEntity(stuff[2])
	if id == "" || name == "" {
		log.Warningf(ctx, "(%s) invalid username %s", op, stuff[2])
		return op, nil, errorInput
	}
	values := opAdd{name: name, id: id, team: strings.ToUpper(stuff[1]), by: r}
	// This operation requires some permission.
	if !userHasPerm(ctx, values.by.id, values.team) {
		log.Warningf(ctx, "(%s) user %s has no perm", op, values.by.name)
		return op, nil, errorNoPerm
	}
	if len(stuff) == 4 {
		values.label = strings.ToLower(stuff[3])
	}
	return op, values, ""
} // }}}

// func decodeRemoveParams {{{

// remove {team} {@slackusername}
//   team - required
//   name - required
//
// This operation requires manager of the team or superuser permission.
func decodeRemoveParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "remove"
	if len(stuff) != 3 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	id, name := decodeUserEntity(stuff[2])
	if id == "" || name == "" {
		log.Warningf(ctx, "(%s) invalid username %s", op, stuff[2])
		return op, nil, errorInput
	}
	values := opRemove{name: name, id: id, team: strings.ToUpper(stuff[1]), by: r}
	// This operation requires permission.
	if !userHasPerm(ctx, values.by.id, values.team) {
		log.Warningf(ctx, "(remove) user %s has no perm", values.by.name)
		return op, nil, errorNoPerm
	}
	return op, values, ""
} // }}}

// func decodeSwapParams {{{

// swap {team} {position_a} {position_b}
//   team - required
//   position_a - required
//   position_b - required
//
// This operation requires manager of the team or superuser permission.
func decodeSwapParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "swap"
	if len(stuff) != 4 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	// Make sure the positions are numeric.
	in, err := strconv.Atoi(stuff[2])
	if err != nil || in < 1 {
		log.Warningf(ctx, "(%s) invalid input - %v", op, stuff)
		return op, nil, errorInput
	}
	values := opSwap{team: strings.ToUpper(stuff[1]), positions: []int{in}, by: r}
	if in, err = strconv.Atoi(stuff[3]); err != nil || in < 1 {
		log.Warningf(ctx, "(%s) invalid input - %v", op, stuff)
		return op, nil, errorInput
	}
	// This operation requires permission.
	if !userHasPerm(ctx, values.by.id, values.team) {
		log.Warningf(ctx, "(%s) user %s has no perm", op, values.by.name)
		return op, nil, errorNoPerm
	}
	values.positions = append(values.positions, in)
	return op, values, ""
} // }}}

// func decodeFlushParams {{{

// flush {team}
//   team - required
//
// This operation requires manager of the team or superuser permission.
func decodeFlushParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "flush"
	if len(stuff) != 2 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	values := opFlush{team: strings.ToUpper(stuff[1]), by: r}
	// This operation requires permission.
	if !userHasPerm(ctx, values.by.id, values.team) {
		log.Warningf(ctx, "(%s) user %s has no perm", op, values.by.name)
		return op, nil, errorNoPerm
	}
	return op, values, ""
} // }}}

// func decodeRegisterParams {{{

// register {team} {@slackusername}
//   team - required
//   name - optional
//
// This operation requires superuser permission.
func decodeRegisterParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "register"
	if len(stuff) < 2 || len(stuff) > 3 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	values := opRegister{team: strings.ToUpper(stuff[1]), by: r}
	if len(stuff) == 3 {
		// The manager info is given, let's decode.
		id, name := decodeUserEntity(stuff[2])
		if id == "" || name == "" {
			log.Warningf(ctx, "(%s) invalid username %s", op, stuff[2])
			return op, nil, errorInput
		}
		values.name = name
		values.id = id
	}
	// This operation requires special permission - only "exempt" users can add a
	// new team.
	if !userIsExempt(ctx, values.by.id) {
		log.Warningf(ctx, "(%s) user %s has no perm", op, values.by.name)
		return op, nil, errorNoPerm
	}
	return op, values, ""
} // }}}

// func decodeUnregisterParams {{{

// unregister {team} {@slackusername}
//   team - required
//   name - optional
//
// This operation requires superuser permission.
func decodeUnregisterParams(ctx context.Context, r opRequestor, stuff []string) (string, interface{}, string) {
	op := "unregister"
	if len(stuff) < 2 || len(stuff) > 3 {
		log.Warningf(ctx, "(%s) invalid # of params - %v", op, stuff)
		return op, nil, errorInput
	}
	values := opUnregister{team: strings.ToUpper(stuff[1]), by: r}
	if len(stuff) == 3 {
		id, name := decodeUserEntity(stuff[2])
		if id == "" || name == "" {
			log.Warningf(ctx, "(%s) invalid username %s", op, stuff[2])
			return op, nil, errorInput
		}
		values.name = name
		values.id = id
	}
	// This operation requires special permission - only "exempt" users can remove a
	// manager from a team.
	if !userIsExempt(ctx, values.by.id) {
		log.Warningf(ctx, "(%s) user %s has no perm", op, values.by.name)
		return op, nil, errorNoPerm
	}
	return op, values, ""
} // }}}

// func decodeUpdateParams {{{
//
// update
//
// This operation updates the requested user's Slack information.
func decodeUpdateParams(ctx context.Context, r opRequestor) (string, interface{}, string) {
	return "update", opUpdate{id: r.id, name: r.name}, ""
} // }}}

// func getCurrentRotation {{{

// Return current oncall rotation for the requested team.
func getCurrentRotation(team string) *oncallProperty {
	oncallMut.RLock()
	defer oncallMut.RUnlock()
	for _, r := range rotations {
		if r.Team == team {
			return r
		}
	}
	return nil
} // }}}
