package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/awebow/ezsock"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/olivere/elastic/v7"
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

	userAuth := app.AuthUserMiddleware(false)
	uploadAuth := middleware.JWTWithConfig(middleware.JWTConfig{
		SigningKey: []byte(app.Config.UploadSignKey),
		ContextKey: "uploadToken",
	})
	allowUnauth := app.AuthUserMiddleware(true)

	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})

	e.GET("/users/:id", app.GetUser)
	e.GET("/users/emails/:email", app.GetEmail)
	e.POST("/users", app.PostUser)
	e.POST("/users/tokens", app.PostToken)
	e.GET("/channels/:id", app.GetChannel)
	e.GET("/channels/:id/videos", app.GetChannelVideos)
	e.GET("/videos", app.GetVideos, allowUnauth)
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
	e.GET("/videos/:id", app.GetVideo, allowUnauth)
	e.GET("/videos/:id/comments", app.GetVideoComments, allowUnauth)

	e.GET("/channels", app.GetChannels)
	e.GET("/channels/subscribed", app.GetSubscribedChannels, userAuth)
	e.POST("/channels", app.PostChannel, userAuth)
	e.GET("/channels/:id/permissions", app.GetChannelPermission, userAuth)
	e.PUT("/channels/:id/picture", app.PutChannelPicture, userAuth)
	e.GET("/channels/:id/subscriptions", app.GetSubscription, userAuth)
	e.POST("/channels/:id/subscriptions", app.PostSubscription, userAuth)
	e.DELETE("/channels/:id/subscriptions", app.DeleteSubscription, userAuth)
	e.POST("/videos", app.PostVideo, userAuth)
	e.PUT("/videos/:id/thumbnail", app.PutThumbnail, userAuth)
	e.GET("/videos/:id/expressions", app.GetExpression, allowUnauth)
	e.PUT("/videos/:id/expressions", app.PutExpression, userAuth)
	e.DELETE("/videos/:id/expressions", app.DeleteExpression, userAuth)
	e.POST("/comments", app.PostComment, userAuth)
	e.DELETE("/comments/:id", app.DeleteComment, userAuth)

	me := e.Group("/users/me", userAuth)
	me.GET("", app.GetMe)
	me.PUT("", app.PutMe)
	me.GET("/channels", app.GetMyChannels)
	me.PUT("/picture", app.PutUserPicture)

	e.HTTPErrorHandler = app.ErrorHandler
	InitValidTrans(e)

	if app.Config.Websocket.Enabled {
		e.GET("/ws", app.ServeWebsocket)
	}

	e.Logger.Fatal(e.Start(app.Config.Listen))
}

type App struct {
	Config struct {
		Listen   string `json:"listen,omitempty"`
		Database struct {
			Host     string `json:"host"`
			User     string `json:"user"`
			Password string `json:"password"`
			Name     string `json:"name"`
		} `json:"database"`
		Redis struct {
			Addr     string `json:"addr"`
			Password string `json:"password"`
			Database int    `json:"database"`
		} `json:"redis"`
		Elasticsearch struct {
			URL          string `json:"url"`
			VideoIndex   string `json:"video_index"`
			ChannelIndex string `json:"channel_index"`
		}
		AllowUserChannel  bool   `json:"allow_user_channel"`
		AuthSignKey       string `json:"auth_sign_key"`
		UploadSignKey     string `json:"upload_sign_key"`
		ULIDConflictRetry int    `json:"ulid_conflict_retry"`
		Storages          struct {
			Video storageConfig `json:"video"`
			Image storageConfig `json:"image"`
		} `json:"storages"`
		Thumbnail      ImageOption   `json:"thumbnail"`
		UserPicture    []ImageOption `json:"user_picture"`
		ChannelPicture []ImageOption `json:"channel_picture"`
		Websocket      struct {
			Enabled      bool `json:"enabled"`
			PingInterval int  `json:"ping_interval"`
			PongTimeout  int  `json:"pong_timeout"`
		} `json:"websocket"`
		SubscriptionBonus int64 `json:"subscription_bonus"`
	}
	db           *sqlx.DB
	es           *elastic.Client
	ws           *ezsock.Server
	videoStorage storage
	imageStorage storage
}

func NewApp() *App {
	app := new(App)

	data, _ := ioutil.ReadFile("config.json")
	json.Unmarshal(data, &app.Config)

	if app.Config.Listen == "" {
		app.Config.Listen = ":80"
	}

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
	if app.Config.Websocket.Enabled {
		app.ws = ezsock.NewServer(ezsock.Config{
			PingInterval: app.Config.Websocket.PingInterval,
			PongTimeout:  app.Config.Websocket.PongTimeout,
			Redis: ezsock.RedisConfig{
				Addr:     app.Config.Redis.Addr,
				Password: app.Config.Redis.Password,
				DB:       app.Config.Redis.Database,
			},
			CheckOrigin: func(r *http.Request) bool { return true },
		})
	}

	es, err := elastic.NewClient(elastic.SetURL(app.Config.Elasticsearch.URL))
	if err != nil {
		fmt.Println(app.Config.Elasticsearch.URL)
		panic("Can not create Elasticsearch client")
	}

	app.es = es

	app.videoStorage, err = createStorage(&app.Config.Storages.Video)
	if err != nil {
		fmt.Println(err)
		panic("Can not create video storage")
	}

	app.imageStorage, err = createStorage(&app.Config.Storages.Image)
	if err != nil {
		fmt.Println(err)
		panic("Can not create image storage")
	}

	return app
}

type ImageOption struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Quality int `json:"quality"`
}
