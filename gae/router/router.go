package router

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/gorilla/mux"
	"github.com/zond/open-messaging/async"
	"github.com/zond/open-messaging/channel"
	"github.com/zond/open-messaging/message"
	"github.com/zond/open-messaging/subscription"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/urlfetch"
)

const (
	maxAge = time.Hour * 24 * 35
)

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

func preflight(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
}

var gcmChannelTask = async.NewTask("gcmChannel", func(c context.Context, channel string) error {
	subscriptions := []subscription.Subscription{}
	_, err := datastore.NewQuery(subscription.Kind).Filter("Channel=", channel).GetAll(c, &subscriptions)
	if err != nil {
		return err
	}
	for len(subscriptions) > 0 {
		n := len(subscriptions)
		if n > 1000 {
			n = 1000
		}
		nextBatch := make([]string, n)
		for index, subscription := range subscriptions[:n] {
			nextBatch[index] = subscription.IID
		}
		if err := gcmSubscribersTask.Schedule(c, time.Duration(0), channel, nextBatch); err != nil {
			return err
		}
		subscriptions = subscriptions[n:]
	}
	return nil
})

var removeIIDsTask = async.NewTask("removeIIDs", func(c context.Context, iids []string) error {
	keys, err := datastore.NewQuery(subscription.Kind).Filter("IID=", iids[0]).KeysOnly().GetAll(c, nil)
	if err != nil {
		return err
	}
	if err := datastore.DeleteMulti(c, keys); err != nil {
		return err
	}
	if len(iids) > 1 {
		return async.FromContext(c).Schedule(c, iids[1:])
	}
	return nil
})

var updateIIDsTask = async.NewTask("updateIIDs", func(c context.Context, oldIIDs, newIIDs []string) error {
	keys, err := datastore.NewQuery(subscription.Kind).Filter("IID=", oldIIDs[0]).KeysOnly().GetAll(c, nil)
	if err != nil {
		return err
	}
	for _, key := range keys {
		err := datastore.RunInTransaction(c, func(c context.Context) error {
			sub := &subscription.Subscription{}
			err := datastore.Get(c, key, sub)
			if err == nil && sub.IID == oldIIDs[0] {
				sub.IID = newIIDs[0]
				if _, err := datastore.Put(c, key, sub); err != nil {
					return err
				}
			} else if err != datastore.ErrNoSuchEntity {
				return err
			}
			return nil
		}, &datastore.TransactionOptions{XG: false})
		if err != nil {
			return err
		}
	}
	if len(oldIIDs) > 1 {
		return async.FromContext(c).Schedule(c, oldIIDs[1:], newIIDs[1:])
	}
	return nil
})

type gcmMessage struct {
	RegistrationIDs []string `json:"registration_ids"`
	Data            struct {
		Channel string
	}
}

type gcmResponse struct {
	MulticastID  int64 `json:"multicast_id"`
	Success      int64 `json:"success"`
	Failure      int64 `json:"failure"`
	CanonicalIDs int64 `json:"canonical_ids"`
	Results      []struct {
		MessageID      string `json:"message_id"`
		RegistrationID string `json:"registration_id"`
		Error          string `json:"error"`
	}
}

var gcmSubscribersTask = async.NewTask("gcmSubscribers", func(c context.Context, delay time.Duration, channel string, iids []string) error {
	nextDelay := (delay * 3) / 2
	if nextDelay == 0 {
		nextDelay = time.Second
	} else if nextDelay > time.Hour {
		nextDelay = time.Hour
	}

	gcmMsg := &gcmMessage{
		RegistrationIDs: iids,
	}
	gcmMsg.Data.Channel = channel
	b, err := json.MarshalIndent(gcmMsg, "", "  ")
	if err != nil {
		return err
	}
	log.Debugf(c, "Request body:\n%s", b)

	req, err := http.NewRequest("POST", "https://gcm-http.googleapis.com/gcm/send", bytes.NewBuffer(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Authorization", "key="+API_KEY)
	log.Debugf(c, "Req:\n%v", spew.Sdump(req))

	client := urlfetch.Client(c)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	if retryAfterHeader := resp.Header.Get("Retry-After"); retryAfterHeader != "" {
		if retryAt, err := time.Parse(time.RFC1123, retryAfterHeader); err == nil {
			nextDelay = retryAt.Sub(retryAt)
		} else if seconds, err := strconv.Atoi(retryAfterHeader); err != nil {
			nextDelay = time.Second * time.Duration(seconds)
		}
	}

	gcmResp := &gcmResponse{}
	if err := json.NewDecoder(resp.Body).Decode(gcmResp); err != nil {
		return err
	}
	log.Debugf(c, "Response:\n%v", spew.Sdump(gcmResp))

	if resp.StatusCode == 200 {
		if gcmResp.Failure > 0 || gcmResp.CanonicalIDs > 0 {
			retries := []string{}
			updateOldIIDs := []string{}
			updateNewIIDs := []string{}
			removes := []string{}
			for index, result := range gcmResp.Results {
				if result.MessageID != "" && result.RegistrationID != "" {
					updateOldIIDs = append(updateOldIIDs, iids[index])
					updateNewIIDs = append(updateNewIIDs, result.RegistrationID)
				} else if result.Error == "Unavailable" {
					retries = append(retries, iids[index])
				} else if result.Error != "" {
					removes = append(removes, iids[index])
				}
			}
			if len(retries) > 0 || len(updateOldIIDs) > 0 || len(removes) > 0 {
				return datastore.RunInTransaction(c, func(c context.Context) error {
					if len(retries) > 0 {
						if err := async.FromContext(c).ScheduleIn(c, nextDelay, channel, retries); err != nil {
							return err
						}
					}
					if len(updateOldIIDs) > 0 {
						if err := updateIIDsTask.Schedule(c, updateOldIIDs, updateNewIIDs); err != nil {
							return err
						}
					}
					if len(removes) > 0 {
						if err := removeIIDsTask.Schedule(c, removes); err != nil {
							return err
						}
					}
					return nil
				}, &datastore.TransactionOptions{XG: true})
			}
		}
		return nil
	} else if resp.StatusCode > 499 {
		log.Errorf(c, "Request error %v, retrying in %v", resp.StatusCode, nextDelay)
		return async.FromContext(c).ScheduleIn(c, nextDelay, channel, iids)
	} else if resp.StatusCode > 399 {
		log.Errorf(c, resp.Status)
		return nil
	}
	return nil
})

func post(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	c := appengine.NewContext(r)

	payload, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if len(payload) == 0 {
		http.Error(w, "No empty messages allowed", 400)
		return
	}

	now := time.Now()
	chKey := datastore.NewKey(c, channel.Kind, mux.Vars(r)["channel"], 0, nil)
	if err := datastore.RunInTransaction(c, func(c context.Context) error {
		ch := &channel.Channel{
			LastMessageAt: now,
		}
		chKey, err := datastore.Put(c, chKey, ch)
		if err != nil {
			return err
		}

		msg := &message.Message{
			ChannelKey: chKey,
			CreatedAt:  now,
			Payload:    payload,
		}
		msgKey := datastore.NewKey(c, message.Kind, "", 0, nil)
		_, err = datastore.Put(c, msgKey, msg)
		if err != nil {
			return err
		}

		return gcmChannelTask.Schedule(c, chKey.StringID())
	}, &datastore.TransactionOptions{
		XG: true,
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func read(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	c := appengine.NewContext(r)
	chKey := datastore.NewKey(c, channel.Kind, mux.Vars(r)["channel"], 0, nil)
	found := []message.Message{}
	query := datastore.NewQuery(message.Kind).Filter("ChannelKey=", chKey).Order("CreatedAt")
	if fromParam := r.URL.Query().Get("from"); fromParam != "" {
		from, err := time.Parse(time.RFC3339, fromParam)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		query = query.Filter("CreatedAt>", from)
	}
	if _, err := query.GetAll(c, &found); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if err := json.NewEncoder(w).Encode(found); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func wipeoutSubscriptions(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	cutoff := time.Now()
	keys, err := datastore.NewQuery(subscription.Kind).Filter("Timeout<", cutoff).KeysOnly().GetAll(c, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := datastore.DeleteMulti(c, keys); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Infof(c, "Deleted %v subscriptions older than %v", len(keys), cutoff)
}

func wipeoutMessages(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	cutoff := time.Now().Add(maxAge)
	keys, err := datastore.NewQuery(message.Kind).Filter("CreatedAt<", cutoff).KeysOnly().GetAll(c, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := datastore.DeleteMulti(c, keys); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Infof(c, "Deleted %v messages older than %v", len(keys), cutoff)
}

func wipeoutChannels(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	cutoff := time.Now().Add(maxAge)
	keys, err := datastore.NewQuery(channel.Kind).Filter("LastMessageAt<", cutoff).KeysOnly().GetAll(c, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := datastore.DeleteMulti(c, keys); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Infof(c, "Deleted %v messages older than %v", len(keys), cutoff)
}

func subscribe(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	c := appengine.NewContext(r)
	sub := &subscription.Subscription{}
	if err := json.NewDecoder(r.Body).Decode(sub); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sub.Channel = mux.Vars(r)["channel"]
	sub.Timeout = time.Now().Add(maxAge)
	if _, err := datastore.Put(c, datastore.NewKey(c, subscription.Kind, sub.Channel+"/"+sub.IID, 0, nil), sub); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if err := json.NewEncoder(w).Encode(sub); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func unsubscribe(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	c := appengine.NewContext(r)
	sub := &subscription.Subscription{}
	if err := json.NewDecoder(r.Body).Decode(sub); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sub.Channel = mux.Vars(r)["channel"]
	keys, err := datastore.NewQuery(subscription.Kind).Filter("IID=", sub.IID).Filter("Channel=", sub.Channel).KeysOnly().GetAll(c, nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := datastore.DeleteMulti(c, keys); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func subscribing(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	c := appengine.NewContext(r)
	sub := &subscription.Subscription{}
	if err := json.NewDecoder(r.Body).Decode(sub); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	sub.Channel = mux.Vars(r)["channel"]
	found := []subscription.Subscription{}
	_, err := datastore.NewQuery(subscription.Kind).Filter("IID=", sub.IID).Filter("Channel=", sub.Channel).GetAll(c, &found)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if err := json.NewEncoder(w).Encode(found); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func init() {
	r := mux.NewRouter()
	r.Methods("OPTIONS").HandlerFunc(preflight)

	wipeout := r.PathPrefix("/wipeout").Subrouter()
	wipeout.Path("/messages").HandlerFunc(wipeoutMessages)
	wipeout.Path("/channels").HandlerFunc(wipeoutChannels)
	wipeout.Path("/subscriptions").HandlerFunc(wipeoutSubscriptions)

	channel := r.PathPrefix("/channels/{channel}").Subrouter()
	channel.Path("/subscribe").Methods("POST").HandlerFunc(subscribe)
	channel.Path("/unsubscribe").Methods("POST").HandlerFunc(unsubscribe)
	channel.Path("/subscribing").Methods("POST").HandlerFunc(subscribing)
	channel.Methods("POST").HandlerFunc(post)
	channel.Methods("GET").HandlerFunc(read)

	http.Handle("/", r)
}
