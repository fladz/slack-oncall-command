"On-Call" management application in Go
===============

Slack slash command endpoint to manage on-call list for different teams in your company/school/organization!

## Modes of Operation
There are different operations supported in this application:

| Operation   | Parameter(s)                | Description                                                             | Permissions Required
|-------------|:----------------------------|:-------------------------------------------------------------------------|:------|
| `list`      | *team*                      | If *team* is provided, show the on-call list for the *team*. List all existing teams and operation manager(s) for each team if *team* is not provided.          | NORMAL+
| `update`    |                             | Update the requested user's Slack profile regardless of its age in cache. | NORMAL+
| `add`       | *team @slackusername label* | Add *@slackusername* to be in that team’s on-call list, at the end. Optional *label* will be set for the *@slackusername*'s entry if given. | MANAGER+
| `swap`      | *team position_A position_B*| Swap” the 2 staff in those positions.                                   | MANAGER+
| `remove`    | *team  @slackusername*      | Remove @slackusername from that team’s on-call list.                    | MANAGER+
| `flush`     | *team*                      | Remove all entries from that team’s on-call list.                       | MANAGER+
| `register`  | *team @slackusername*       | Create a new team, and give *@slackusername* permissions to manage that *team*’s on-call list. `register` can also be used to add an additional manager to an existing team. | SUPERUSER
| `unregister` | *team @slackusername*      | Remove *@slackusername* from being listed as that *team*’s manager, and remove *@slackusername*’s permissions to manage that *team*’s on-call list. If *@slackusername* is not specified, the entire *team* and it’s on-call list will be completely removed. | SUPERUSER

## Permission Levels

There are 3 permission levels in this application:

- NORMAL

All Slack users are given this level. The only operations this level of users can run are `list` and `update`.

- MANAGER

This permission will be given when *@slackusername* is assigned to be a manager of one (or more) *team*.
This level of users can run all operations NORMAL users can run plus `add`, `remove`, `swap` and `flush`.

- SUPERUSER

This permission will be given to all Slack admins (member of @admins) by default. Individual *@slackusername* can also be given this permission level if the *@slackusername* is configured to be SUPERUSER. (See below "Configuration" section for more detail.) This level of users can run all operation MANAGER users can run plus `register` and `unregister`.

## Configuration
Below is a configuration options to be used inside *env_variables* section in the .yaml file:

| Key   | Required     | Description                                                             |
|:------|:-------------|:------------------------------------------------------------------------|
| slack_command_token | Yes | Token to be used to verify identity of request initiator. Generate via Slack admin console.
| slack_api_token     | Yes | Token to be used to talk to Slack API.
| command_endpoint    | No  | Endpoint of this on-call command. Default is "/oncall".
| operation_timeout   | No  | Per-operation timeout. Default is "3s" (3 seconds).
| superusers          | No  | Comma-separated list of Slack usernames that will automatically be given SUPERUSER permission.
| demote_admins       | No  | If you don't want Slack admins (member of @admins) to be given SUPERUSER permission, set this to "true". Note even if you set this to "true", if there is no one configured in "superusers" option this option will be disabled. Default "false".
| cache_timeout       | No  | Duration to refresh Slack user profile cache. The only user profile value this oncall application cares is a phone number. Set proper value based on how often phone numbers would change. Default is "3d" (3 days).
| timezone            | No  | Timezone used to display each on-call list's last updated timestamp. Default "UTC".
| input_error_emoji   | No  | Custom emoji to be displayed along with brief error message when there is a problem with user input. Since default emoji is kind of boring, if you want to have some fun you can set your favorite emoji here! Default ":exclamation:".
| external_error_emoji | No | Custom emoji to be displayed along with brief error message when there is a problem in external services (Slack API or Google Datastore). Since default emoji is kind of boring, if you want to have some fun you can set your favorite emoji here! Default ":negative_squared_cross_mark:".

## Implementation Details
This application is made to run on Google AppEngine, on-call details will be saved in Google Datastore. Google Datastore is used purely for backup purpose.

This application manages on-call details and Slack user profiles in memory, when on-call list is updated it'll update both Google Datastore and memory, when on-call detail is queried it'll use in-memory data.

Slack user profile information is cached in-memory. Currently it refreshes the cache when (1) the user data is accessed after cache expiration, or (2) *refresh* command is sent.


## Prerequisites

1. Set up a project inside Google AppEngine.
2. Configure in Slack to send on-call slash command to be sent to the AppEngine project you created.
3. Generate Slack Tokens. (One for on-call command, other for Slack API)

## Installation

Once you have all prerequisites above ready, install this application

    $ go get github.com/fladz/slack-oncall-command

Then deploy to your Google AppEngine project

    $ goapp deploy -application {YOUR_PROJECT} -version go1 .

## TODO

- Add Slack event listener to monitor user profile change status (user_change)
