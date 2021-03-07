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
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello, World!")
	})
	router.POST("/users", app.PostUsers)
	router.POST("/users/tokens", app.PostTokens)
	router.GET("/channels/:id", app.GetChannelById)

	authorized := router.Group("/", app.AuthMiddleware())
	authorized.POST("/channels", app.PostChannels)

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
		AuthSignKey       string `json:"auth_sign_key"`
		ULIDConflictRetry int    `json:"ulid_conflict_retry"`
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
