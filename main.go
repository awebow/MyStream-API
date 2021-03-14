package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/gin-gonic/gin"
	ut "github.com/go-playground/universal-translator"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func main() {
	app := NewApp()

	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "*")
		c.Header("Access-Control-Allow-Headers", "*")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	})

	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})
	router.POST("/users", app.PostUser)
	router.POST("/users/tokens", app.PostToken)
	router.GET("/channels/:id", app.GetChannelById)
	router.GET("/channels/:id/videos", app.GetChannelVideos)
	router.GET("/videos", app.GetVideos)
	router.GET("/videos/:id", app.GetVideo)
	router.PUT("/videos/:id", app.PutVideo)
	router.GET("/videos/:id/comments", app.GetVideoComments)

	authorized := router.Group("/", app.AuthMiddleware(false))
	authorized.POST("/channels", app.PostChannel)
	authorized.POST("/videos", app.PostVideo)
	authorized.PUT("/videos/:id/thumbnail", app.PutThumbnail)
	authorized.POST("/comments", app.PostComment)
	authorized.DELETE("/comments/:id", app.DeleteComment)

	me := authorized.Group("users/me")
	me.GET("", app.GetMe)
	me.GET("/channels", app.GetMyChannels)

	router.Run(app.Config.Listen...)
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
		AuthSignKey       string   `json:"auth_sign_key"`
		UploadSignKey     string   `json:"upload_sign_key"`
		ULIDConflictRetry int      `json:"ulid_conflict_retry"`
		StoreCommand      []string `json:"store_cmd"`
		Thumbnail         struct {
			Width   int `json:"width"`
			Height  int `json:"height"`
			Quality int `json:"quality"`
		} `json:"thumbnail"`
	}
	db         *sqlx.DB
	validTrans ut.Translator
}

func NewApp() *App {
	app := new(App)
	app.InitValidTrans()

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
