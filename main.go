package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	app := NewApp()

	e := echo.New()
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodDelete,
			http.MethodPatch,
			http.MethodOptions,
			http.MethodHead,
		},
		AllowHeaders: []string{"*"},
	}))

	userAuth := middleware.JWT([]byte(app.Config.AuthSignKey))
	uploadAuth := middleware.JWTWithConfig(middleware.JWTConfig{
		SigningKey: []byte(app.Config.UploadSignKey),
		ContextKey: "uploadToken",
	})

	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	e.GET("/users/:id", app.GetUser)
	e.POST("/users", app.PostUser)
	e.POST("/users/tokens", app.PostToken)
	e.GET("/channels/:id", app.GetChannel)
	e.GET("/channels/:id/videos", app.GetChannelVideos)
	e.GET("/videos", app.GetVideos)
	e.PUT("/videos/:id", app.PutVideo, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userErr := userAuth(func(echo.Context) error { return nil })(c)
			uploadErr := uploadAuth(func(echo.Context) error { return nil })(c)

			if userErr != nil && uploadErr != nil {
				return echo.ErrUnauthorized
			}

			return next(c)
		}
	})
	e.GET("/videos/:id", app.GetVideo)
	e.GET("/videos/:id/comments", app.GetVideoComments)

	authorized := e.Group("", userAuth)
	authorized.POST("/channels", app.PostChannel)
	authorized.PUT("/channels/:id/picture", app.PutChannelPicture)
	authorized.POST("/videos", app.PostVideo)
	authorized.PUT("/videos/:id/thumbnail", app.PutThumbnail)
	authorized.POST("/comments", app.PostComment)
	authorized.DELETE("/comments/:id", app.DeleteComment)

	me := authorized.Group("/users/me")
	me.GET("", app.GetMe)
	me.GET("/channels", app.GetMyChannels)
	me.PUT("/picture", app.PutUserPicture)

	e.HTTPErrorHandler = app.ErrorHandler
	InitValidTrans(e)
	e.Logger.Fatal(e.Start(app.Config.Listen[0]))
}

type App struct {
	Config struct {
		Listen   []string `json:"listen"`
		Database struct {
			Host     string `json:"host"`
			User     string `json:"user"`
			Password string `json:"password"`
			Name     string `json:"name"`
		} `json:"database"`
		AuthSignKey       string        `json:"auth_sign_key"`
		UploadSignKey     string        `json:"upload_sign_key"`
		ULIDConflictRetry int           `json:"ulid_conflict_retry"`
		StoreCommand      []string      `json:"store_cmd"`
		Thumbnail         ImageOption   `json:"thumbnail"`
		UserPicture       []ImageOption `json:"user_picture"`
		ChannelPicture    []ImageOption `json:"channel_picture"`
	}
	db *sqlx.DB
}

func NewApp() *App {
	app := new(App)

	data, _ := ioutil.ReadFile("config.json")
	json.Unmarshal(data, &app.Config)

	db, err := sqlx.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true",
		app.Config.Database.User,
		app.Config.Database.Password,
		app.Config.Database.Host,
		app.Config.Database.Name,
	))

	if err != nil {
		panic("Can not open the database.")
	}

	app.db = db

	return app
}

type ImageOption struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Quality int `json:"quality"`
}
