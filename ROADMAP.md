# Features & roadmap
* Matrix → WhatsApp
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
  * [ ] Message redactions<sup>[1]</sup>
  * [ ] Presence<sup>[4]</sup>
  * [ ] Typing notifications<sup>[4]</sup>
  * [ ] Read receipts<sup>[4]</sup>
  * [ ] Power level
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [ ] Leave
    * [ ] Kick
  * [ ] Room metadata changes
    * [x] Name
    * [ ] Avatar<sup>[1]</sup>
    * [ ] Topic<sup>[1]</sup>
  * [ ] Initial room metadata
* WhatsApp → Matrix
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
  * [ ] Chat types
    * [x] Private chat
    * [x] Group chat
    * [ ] Broadcast list<sup>[2]</sup>
  * [ ] Message deletions<sup>[1]</sup>
  * [x] Avatars
  * [x] Presence
  * [x] Typing notifications
  * [x] Read receipts
  * [x] Admin/superadmin status
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [ ] Leave
    * [ ] Kick
  * [x] Group metadata changes
    * [x] Title
    * [x] Avatar
    * [x] Description
  * [x] Initial group metadata
  * [ ] User metadata changes
    * [ ] Display name<sup>[3]</sup>
    * [x] Avatar
  * [x] Initial user metadata
    * [x] Display name
    * [x] Avatar
* Misc
  * [x] Automatic portal creation
    * [x] At startup
    * [ ] When receiving invite<sup>[2]</sup>
    * [x] When receiving message
  * [ ] Private chat creation by inviting Matrix puppet of WhatsApp user to new room
  * [ ] Option to use own Matrix account for messages sent from WhatsApp mobile/other web clients
  * [x] Shared group chat portals

<sup>[1]</sup> May involve reverse-engineering the WhatsApp Web API and/or editing go-whatsapp  
<sup>[2]</sup> May already work  
<sup>[3]</sup> May not be possible  
<sup>[4]</sup> Requires [matrix-org/synapse#2954](https://github.com/matrix-org/synapse/issues/2954) or Matrix puppeting
