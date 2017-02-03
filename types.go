package slackoncallbot

import (
	"google.golang.org/appengine/datastore"
	"sync"
	"time"
)

// Request parameters from Slack.
//
// Example request from Slack look like -
//
// token=ItoB7oEyZIbNmHPfxHQ2GrbC
// team_id=T0001
// team_domain=example
// channel_id=C2147483705
// channel_name=test
// user_id=U2147483697
// user_name=Steve
// command=/weather
// text=94070
// response_url=https://hooks.slack.com/commands/1234/5678
type slackCommandParams struct {
	Token       string `schema:"token"`
	TeamId      string `schema:"team_id"`
	TeamDomain  string `schema:"team_domain"`
	ChannelId   string `schema:"channel_id"`
	ChannelName string `schema:"channel_name"`
	UserId      string `schema:"user_id"`
	UserName    string `schema:"user_name"`
	Command     string `schema:"command"`
	Text        string `schema:"text"`
	ResponseURL string `schema:"response_url"`
}

type slackResponse struct {
	Type        string       `json:"response_type,omitempty"`
	Text        string       `json:"text,omitempty"`
	Attachments []attachment `json:"attachments,omitempty"`
}

// Slack "attachment" response struct.
// Note this is much shorter version of the full struct as we don't need
// such a fancy display for oncall.
type attachment struct {
	Title  string `json:"title,omitempty"`
	Text   string `json:"text"`
	Color  string `json:"color,omitempty"`
	Footer string `json:"footer,omitempty"`
}

// Summarized user information we need for oncall operations.
type slackUser struct {
	name        string
	isSuperuser bool
	isAdmin     bool
	isManager   int
	phone       string
	// Timestamp of the user retrieved from Slack API
	retrieved time.Time
}

// Per-team information.
type oncallProperties []*oncallProperty
type oncallProperty struct {
	Key       *datastore.Key     `datastore:"key"`
	Team      string             `datastore:"team"`
	Managers  []ManagerProperty  `datastore:"managers"`
	Rotations []RotationProperty `datastore:"users"`
	Updated   time.Time          `datastore:"updated"`
	UpdatedBy string             `datastore:"updated_by"`
}
type ManagerProperty struct {
	Name string `datastore:"manager_name"`
	Id   string `datastore:"manager_id"`
}
type RotationProperty struct {
	Name  string `datastore:"name"`
	Id    string `datastore:"id"`
	Label string `datastore:"label"`
}

const (
	// Datastore kind for oncall states.
	oncallKind = "oncall_list"
	// Short representation of modified timestamp.
	dateFormat = "2006-01-02 15:04"
)

var (
	// Token used to verify identity of incoming oncall requests from Slack.
	slackCommandToken string
	// Token used to call Slack API.
	slackAPIToken string
	// Actual command to trigger oncall operations. Default "/oncall"
	command string = "/oncall"
	// Slack user data cache duration.
	cacheTimeout time.Duration
	// Timeout per operation.
	// This comes from configuration if set. Default 3 seconds.
	opTimeout time.Duration
	// Timezone to use for updated timestamp. Default GMT.
	timezone *time.Location
	// List of Slack user names to be treated as "superuser"
	superusers []string
	// Flag to tell us if Slack admins shouldn't be given superuser permission automatically.
	adminDisabled bool
	// Emoji to be used when underprivileged users try to run permission-required
	// commands, or invalid inputs.
	humanErrorEmoji = ":exclamation:"
	// Emoji to be used when an error is returned from external sources such as
	// AppEngine, Datastore and/or Slack API.
	externalErrorEmoji = ":negative_squared_cross_mark:"
	// Just for another fun.
	defaultColor = "EF203D"
	// List of users assigned in oncall rotation per team.
	rotations oncallProperties
	// Mutex lock for accessing oncall rotations.
	oncallMut sync.RWMutex
	// Internal list of Slack users.
	// Key is Slack user_id
	slackUsers map[string]*slackUser
	// Mutex lock for accessing Slack user map.
	slackMut sync.RWMutex
	// Generic help text
	helpList       string
	helpAdd        string
	helpRemove     string
	helpSwap       string
	helpFlush      string
	helpRegister   string
	helpUnregister string
	helpUpdate     string
)

// Operation requestor name and id.
type opRequestor struct {
	name, id string
}

// Values needed for "add" operation.
type opAdd struct {
	// Name of user to be added to rotation.
	name string
	// Id of user to be added to rotation.
	id string
	// Team to be updated.
	team string
	// Optional custom label.
	label string
	// Requestor information.
	by opRequestor
}

// Values needed for "swap" operation
type opSwap struct {
	// Team to be updated.
	team string
	// Positions to update.
	positions []int
	// Requestor information.
	by opRequestor
}

// Values needed for "list" operation.
type opList struct {
	// Optional, list up oncall rotation for this team.
	team string
}

// Values needed for "remove" operation.
type opRemove struct {
	// Name of user to be removed from rotation.
	name string
	// Id of user to be removed from rotation.
	id string
	// Name of team the requested user will be removed from.
	team string
	// Requestor information.
	by opRequestor
}

// Values needed for "flush" operation.
type opFlush struct {
	// team to be cleared its rotation.
	team string
	// Requestor information.
	by opRequestor
}

// Values needed for "register" operation.
type opRegister struct {
	// team to be registered in our managed teams.
	team string
	// Manager of this team.
	name string
	// Id of the manager.
	id string
	// Requestor information.
	by opRequestor
}

// Values needed for "unregister" operation.
type opUnregister struct {
	// Team to remove the manager from.
	team string
	// Manager to be removed from this team.
	name string
	// Id of manager to be removed from this team.
	id string
	// Requestor information.
	by opRequestor
}

// Values needed for "update" operation.
type opUpdate struct {
	id   string
	name string
}

// Sort function for the team list.
func (r oncallProperties) Len() int {
	return len(r)
}
func (r oncallProperties) Less(i, j int) bool {
	return r[i].Team < r[j].Team
}
func (r oncallProperties) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

// Error messages
var (
	// Bad user input
	errorInput string
	// External error
	errorExternal string
	// Permission error
	errorNoPerm string
	// Requested team not exist in managed team list
	errorNoTeam string
	// Requested team has no manager
	errorNoManager string
	// Requested user doesn't have phone number set in Slack profile
	errorNoPhone string
	// Requested user not exist in Slack
	errorNoProfile string
	// Requested team has no oncall rotation yet
	errorNoRotation string
)

// Context key
type ctxKey int

const ctxKeyUserId ctxKey = 1
