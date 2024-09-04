# V2 Features & roadmap
* Matrix → WhatsApp
	* [x] Message content
		* [x] Plain text
		* [x] Formatted messages
		* [x] Location messages
		* [x] Media/files
		* [x] Replies
	* [x] Message redactions
	* [x] Edits
	* [x] Reactions
	* [x] Typing notifications
	* [x] Read receipts
	* [ ] Power level
	* [ ] Membership actions
		* [ ] Invite
		* [ ] Leave
		* [ ] Kick
	* [ ] Room metadata changes
		* [ ] Name
		* [ ] Avatar
		* [ ] Topic
	* [ ] Initial room metadata
* WhatsApp → Matrix
	* [x] Message content
		* [x] Plain text
		* [x] Formatted messages
		* [x] Media/files
		* [x] Location messages
		* [x] Contact messages
		* [x] Replies
	* [ ] Chat types
		* [x] Private chat
		* [x] Group chat
		* [ ] Communities
		* [ ] Status broadcast
		* [ ] Broadcast list (not currently supported on WhatsApp web)
	* [x] Message deletions
	* [x] Edits
	* [x] Reactions
	* [ ] Avatars
	* [x] Typing notifications
	* [x] Read receipts
	* [ ] Admin/superadmin status
	* [ ] Membership actions
		* [ ] Invite
		* [ ] Join
		* [ ] Leave
		* [ ] Kick
	* [ ] Group metadata changes
		* [ ] Title
		* [ ] Avatar
		* [ ] Description
	* [ ] Initial group metadata
	* [ ] User metadata changes
		* [ ] Display name
		* [ ] Avatar
	* [ ] Initial user metadata
		* [ ] Display name
		* [ ] Avatar
* Misc
	* [ ] Automatic portal creation
		* [ ] After login
		* [x] When added to group
		* [x] When receiving message
	* [ ] Private chat creation by inviting Matrix puppet of WhatsApp user to new room
	* [ ] Option to use own Matrix account for messages sent from WhatsApp mobile/other web clients
	* [x] Shared group chat portals

# Missing in bridgev2
- Polls (both ways)
- Poll votes (both ways)
- presence (both ways)
- Creating WA communities from m.space rooms
