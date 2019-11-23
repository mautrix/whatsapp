# Authentication

## Logging In

 1. Start a chat with the bridge bot.
     - You can for instance create a new room and type `/invite @whatsappbot:matrix.example.com` to invite the bot (assuming you did not change the bot username in config and your Synapse server is running at matrix.example.com).
    - The bot should say "This room has been registered as your bridge management/status room." if you started the chat correctly.
    - If the bot doesn't work check the logs from the bridge.
    - NOTE: Riot (and other clients) might report that the bot does not exist, just invite it anyway.
 2. Send `login` to the bot
 3. Log in by scanning the QR code. If the code expires before you scan it, the bridge will send an error to notify you.
    1. Open WhatsApp on your phone.
    2. Tap Menu or Settings and select WhatsApp Web.
    3. Point your phone at the image sent by the bot to capture the code.
    - **NOTE**: The bridge will cycle through multiple QR images by default. You need to be ready to scan the images.
 4. Finally, the bot should inform you of a successful login and the bridge should start creating portal rooms for all your WhatsApp groups and private chats.
 5. You might be interested in using the `login-matrix <access token>` command for Matrix puppeting (see https://github.com/tulir/mautrix-whatsapp/issues/16). In Riot, you can find your access token under Settings > Help > Access token.

Please note that the bridge uses the web API, so your phone must be connected to the internet for the bridge to work. The WhatsApp app doesn't need to be running all the time, but it needs to be allowed to wake up when receiving messages (should work by default). The web API is used instead of the main client API to avoid bans (WhatsApp will ban unofficial clients).

## Logging Out

Simply run the `logout` management command.
