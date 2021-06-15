package main

import (
	"strings"

	"github.com/labstack/echo/v4"
)

func (app *App) ServeWebsocket(c echo.Context) error {
	client, err := app.ws.Handle(c.Response(), c.Request())
	if err != nil {
		return err
	}

	userID, _ := app.AuthUser(c.QueryParam("authorization"))
	client.On("join", new(string), func(data interface{}) {
		if p, ok := data.(*string); ok {
			if s := strings.Split(*p, "/"); s[0] == "video" && s[2] == "encode" {
				video, err := app.SelectVideo(s[1])
				if err != nil {
					return
				}

				ownerID, err := app.SelectChannelOwnerID(video.ChannelID)
				if err != nil {
					return
				}

				if ownerID == userID {
					client.Subscribe(*p)
				}
			}
		}
	})
	client.Emit("ready", nil)

	return nil
}
