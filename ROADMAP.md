# Features & roadmap
* Matrix → WhatsApp
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [x] Replies
  * [x] Message redactions
  * [x] Presence
  * [x] Typing notifications
  * [x] Read receipts
  * [ ] Power level
  * [ ] Membership actions
    * [ ] Invite
    * [ ] Join
    * [ ] Leave
    * [ ] Kick
  * [ ] Room metadata changes
    * [x] Name
    * [ ] Avatar<sup>[1]</sup>
    * [x] Topic
  * [ ] Initial room metadata
* WhatsApp → Matrix
  * [x] Message content
    * [x] Plain text
    * [x] Formatted messages
    * [x] Media/files
    * [ ] Location messages
    * [x] Replies
  * [ ] Chat types
    * [x] Private chat
    * [x] Group chat
    * [ ] Broadcast list<sup>[2]</sup>
  * [x] Message deletions
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
  * [x] Option to use own Matrix account for messages sent from WhatsApp mobile/other web clients
  * [x] Shared group chat portals

<sup>[1]</sup> May involve reverse-engineering the WhatsApp Web API and/or editing go-whatsapp  
<sup>[2]</sup> May already work  
<sup>[3]</sup> May not be possible  
