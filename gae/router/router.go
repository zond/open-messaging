package router

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/zond/open-messaging/channel"
	"github.com/zond/open-messaging/message"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
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

func post(w http.ResponseWriter, r *http.Request) {
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
	if err := datastore.RunInTransaction(c, func(c context.Context) error {
		chKey := datastore.NewKey(c, channel.Kind, mux.Vars(r)["channel"], 0, nil)
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
		return err
	}, &datastore.TransactionOptions{
		XG: true,
	}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func read(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	chKey := datastore.NewKey(c, channel.Kind, mux.Vars(r)["channel"], 0, nil)
	found := []message.Message{}
	query := datastore.NewQuery(message.Kind).Filter("ChannelKey=", chKey)
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

func init() {
	r := mux.NewRouter()
	r.Methods("OPTIONS").HandlerFunc(preflight)
	wipeout := r.PathPrefix("/wipeout").Subrouter()
	wipeout.Path("/message").HandlerFunc(wipeoutMessages)
	wipeout.Path("/channel").HandlerFunc(wipeoutChannels)
	messages := r.Path("/{channel}").Subrouter()
	messages.Methods("POST").HandlerFunc(post)
	messages.Methods("GET").HandlerFunc(read)
	http.Handle("/", r)
}
