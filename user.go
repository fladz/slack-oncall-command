package slackoncallbot

import (
	"github.com/nlopes/slack"
	"golang.org/x/net/context"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
	"strings"
	"time"
)

// func userHasPerm {{{

// Check if the requestor is a manager of the requested team, or an exempt user.
func userHasPerm(ctx context.Context, id, team string) bool {
	// If the user is exempt, let them update.
	if userIsExempt(ctx, id) {
		return true
	}

	// If the user is a manager of the team, let them update.
	var managers []ManagerProperty
	oncallMut.RLock()
	for _, r := range rotations {
		if r.Team == team {
			managers = r.Managers
		}
	}
	oncallMut.RUnlock()
	if len(managers) == 0 {
		return false
	}
	for _, manager := range managers {
		if manager.Id == id {
			return true
		}
	}

	return false
} // }}}

// func userIsExempt {{{

// Check if the requested user is either superuser or Slack admin.
func userIsExempt(ctx context.Context, id string) bool {
	if len(superusers) > 0 {
		// If superusers slice is not yet empty, it means the users are not
		// loaded into our Slack user map, so do the initial load to get their user_ids.
		if err := loadSuperusers(ctx); err != nil {
			log.Warningf(ctx, "(userIsExempt) error loading superusers - %s", err)
			return false
		}
	}

	// Get user detail to check flags.
	user, err := getSlackUserDetail(ctx, id)
	if err != nil {
		log.Warningf(ctx, "error getting user detail (%s) - %s", id, err)
		return false
	}
	if user == nil {
		log.Warningf(ctx, "Slack inactive user trying to hack us!!! %d", id)
		return false
	}

	// User is superuser, let them go through.
	if user.isSuperuser {
		return true
	}
	// Slack admins are superuser too, and this user is Slack admin. Approved!
	if !adminDisabled && user.isAdmin {
		return true
	}
	// Noep!
	return false
} // }}}

// func decodeUserEntity {{{

// Decode expanded user entity from Slack into user_id and user_name.
//
// The format should be -
// <@{SLACK_USER_ID}|{SLACK_USER_NAME}>
//
// For this to work, the "expanded entity" needs to be toggled.
// https://api.slack.com/slash-commands
func decodeUserEntity(entity string) (string, string) {
	// Kinda stupidly done here .. let's check if the string has items we require.
	if entity[0] != '<' || entity[len(entity)-1] != '>' || !strings.Contains(entity, "|") {
		return "", ""
	}
	// Get rid of leading and trailing brackets..
	entity = entity[1:]
	entity = entity[:len(entity)-1]
	items := strings.Split(entity, "|")
	if len(items) != 2 {
		return "", ""
	}
	if items[0][0:2] != "@U" {
		return "", ""
	}
	return items[0][1:], items[1]
} // }}}

// func userConvert {{{

// Convert *slack.User into our slackUser struct.
func userConvert(s *slack.User) *slackUser {
	return &slackUser{
		name:      s.Name,
		isAdmin:   s.IsAdmin,
		phone:     s.Profile.Phone,
		retrieved: time.Now(),
	}
} // }}}

// func getSlackUser {{{

// Call Slack API to get user information of requested user.
func getSlackUser(ctx context.Context, id string) (*slackUser, error) {
	c := slack.New(slackAPIToken)
	slack.HTTPClient.Transport = &urlfetch.Transport{Context: ctx}
	user, err := c.GetUserInfo(id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, nil
	}

	// Make sure the user is not bot and active.
	if user.IsBot || user.Deleted {
		return nil, nil
	}

	return userConvert(user), nil
} // }}}

// func getSlackUserDetail {{{

// Get detail of requested user.
// First try finding the user in memory. If the user doesn't exist or the user data was retrieved
// after the cache expiry, get the user information from Slack API.
func getSlackUserDetail(ctx context.Context, id string) (*slackUser, error) {
	slackMut.RLock()
	user := slackUsers[id]
	slackMut.RUnlock()
	var err error

	if user != nil {
		// If the data is too old, refresh.
		if time.Now().After(user.retrieved.Add(cacheTimeout)) {
			// Too old, get a new one.
			newuser, err := getSlackUser(ctx, id)
			if err != nil {
				// Error refreshing user cache, return current user data.
				return user, err
			}
			// Reset the map value.
			newuser.isSuperuser = user.isSuperuser
			slackMut.Lock()
			slackUsers[id] = newuser
			slackMut.Unlock()
			return newuser, nil
		}
		return user, nil
	}

	// User not exists :(
	// Let's check Slack on this..
	if user, err = getSlackUser(ctx, id); err != nil {
		return nil, err
	}
	if user == nil {
		slackMut.Lock()
		delete(slackUsers, id)
		slackMut.Unlock()
		return nil, nil
	}

	// Got the info, let's save and return.
	slackMut.Lock()
	slackUsers[id] = user
	slackMut.Unlock()

	return user, nil
} // }}}

// func loadSuperusers {{{

// Initial load of configured superusers.
// Since the list of users in configuration is all user_name but we need user_id so the detail
// can be saved in our user_id key Slack user map.
func loadSuperusers(ctx context.Context) error {
	c := slack.New(slackAPIToken)
	slack.HTTPClient.Transport = &urlfetch.Transport{Context: ctx}
	users, err := c.GetUsers()
	if err != nil {
		return err
	}

	slackMut.Lock()
	defer slackMut.Unlock()
	for _, user := range users {
		for idx, name := range superusers {
			if name == user.Name {
				// If the user is non-human or inactive, ignore.
				if !user.IsBot && !user.Deleted {
					// Let's save the user.
					slackUsers[user.ID] = &slackUser{
						name:        user.Name,
						isSuperuser: true,
						isAdmin:     user.IsAdmin,
						phone:       user.Profile.Phone,
						retrieved:   time.Now(),
					}
					log.Infof(ctx, "loaded superuser detail - %s", user.Name)
				}
				superusers = append(superusers[:idx], superusers[idx+1:]...)
				break
			}
		}
		if len(superusers) == 0 {
			return nil
		}
	}

	return nil
} // }}}
