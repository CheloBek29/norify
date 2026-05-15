package main

import (
	"net/http"
	"strconv"

	"github.com/norify/platform/packages/go-common/httpapi"
	"github.com/norify/platform/packages/go-common/reliability"
)

func main() {
	mux := httpapi.NewMux(httpapi.Service{Name: "notification-error-service", Version: "0.1.0"})
	mux.HandleFunc("/errors/channel", channelError)
	_ = httpapi.Listen("notification-error-service", mux)
}

func channelError(w http.ResponseWriter, r *http.Request) {
	affected, _ := strconv.Atoi(r.URL.Query().Get("affected"))
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "telegram"
	}
	httpapi.WriteJSON(w, http.StatusOK, reliability.BuildChannelError(channel, affected))
}
