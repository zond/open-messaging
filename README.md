open-messaging
=====

Open messaging on top of App Engine and GCM.

### Usage

#### Posting messages

To post a message to {channel name} with payload {message}:

`curl -XPOST https://open-messaging.appspot.com/channels/{channel name} -d "{message}"`

#### Reading messages

To get JSON containing creation time and base64 encoded payload of all messages in {channel name}:

`curl https://open-messaging.appspot.com/channels/{channel name}`

To get all messages with creation time greater than a given time:

`curl https://open-messaging.appspot.com/channels/{channel name}?from=2016-04-10T11:44:25.122805Z`

#### Subscribing to messages

To get notifications to {GCM InstanceID} whenever new messages are posted to {channel name}:

`curl -XPOST https://open-messaging-appspot.com/channels/{channel name}/subscribe -d '{"IID":"{GCM InstanceID}"}`

To unsubscribe:

`curl -XPOST https://open-messaging-appspot.com/channels/{channel name}/unsubscribe -d '{"IID":"{GCM InstanceID}"}`

#### Demo

To test the GCM delivery of notifications, try the demo page at https://open-messaging.appspot.com/.
