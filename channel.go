package main

import (
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
)

type Channel struct {
	ID            string     `json:"id" db:"id"`
	Name          string     `json:"name" db:"name"`
	Description   string     `json:"description" db:"description"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) GetChannelById(c *gin.Context) {
	sql := "SELECT `id`, `name`, `description`, `created_at`, `deactivated_at` FROM channels WHERE `id`=?"
	rows, err := app.db.Queryx(sql, c.Param("id"))
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows.Next() {
		channel := Channel{}
		err = rows.StructScan(&channel)
		if err != nil {
			app.HandleError(c, err)
			return
		}

		c.JSON(http.StatusOK, channel)
	} else {
		app.HandleError(c, NotFoundError("channel"))
	}
}

func (app *App) PostChannel(c *gin.Context) {
	body := struct {
		Name        string `json:"name" binding:"required,max=100"`
		Description string `json:"description"`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	sql := "INSERT INTO channels (`id`, `name`, `description`, `owner`, `created_at`) VALUES (?, ?, ?, ?, ?)"
	stmt, err := app.db.Prepare(sql)
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer stmt.Close()

	var id ulid.ULID
	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	inserted := false
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err := stmt.Exec(id.String(), body.Name, body.Description, c.GetString("UserID"), now)

		if err == nil {
			inserted = true
			break
		}
	}

	if inserted {
		c.JSON(http.StatusOK, gin.H{"id": id})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "Unknown server error."})
	}
}

func (app *App) GetChannelVideos(c *gin.Context) {
	channelID := c.Param("id")

	sql := "SELECT * FROM videos WHERE `channel_id`=? AND `status`='ACTIVE'"
	rows, err := app.db.Unsafe().Queryx(sql, channelID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	videos := []Video{}
	for rows.Next() {
		v := Video{}
		err = rows.StructScan(&v)
		videos = append(videos, v)
	}

	c.JSON(http.StatusOK, videos)
}
