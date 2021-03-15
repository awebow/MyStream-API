package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/oklog/ulid/v2"
)

type Channel struct {
	ID            string     `json:"id" db:"id"`
	Name          string     `json:"name" db:"name"`
	Description   string     `json:"description" db:"description"`
	Picture       *string    `json:"picture" db:"picture"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectChannel(id string) (channel *Channel, err error) {
	channel = &Channel{}

	var rows *sqlx.Rows
	rows, err = app.db.Unsafe().Queryx("SELECT * FROM channels WHERE `id`=?", id)
	if err != nil {
		return
	}

	if rows.Next() {
		err = rows.StructScan(&channel)
	} else {
		err = NotFoundError("channel")
	}
	return
}

func (app *App) GetChannelById(c *gin.Context) {
	channel, err := app.SelectChannel(c.Param("id"))
	if err != nil {
		app.HandleError(c, err)
	} else {
		c.JSON(http.StatusOK, channel)
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

func (app *App) PutChannelPicture(c *gin.Context) {
	channelID := c.Param("id")

	header, err := c.FormFile("file")
	if err != nil {
		app.HandleError(c, err)
		return
	}

	file, err := header.Open()
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer file.Close()

	img, err := imaging.Decode(file)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	dir, err := ioutil.TempDir("", "picture")
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer os.RemoveAll(dir)

	for _, o := range app.Config.UserPicture {
		output, err := os.Create(fmt.Sprintf("%s/%dx%d.jpg", dir, o.Width, o.Height))
		if err != nil {
			app.HandleError(c, err)
			return
		}

		resized := imaging.Fill(img, o.Width, o.Height, imaging.Center, imaging.Lanczos)
		err = imaging.Encode(output, resized, imaging.JPEG, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
		if err != nil {
			output.Close()
			app.HandleError(c, err)
			return
		}

		if err = output.Close(); err != nil {
			app.HandleError(c, err)
			return
		}
	}

	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	fileName := "c" + channelID + ulid.MustNew(ulid.Timestamp(now), entropy).String()

	if err = app.StoreFile(dir, "images/"+fileName); err != nil {
		app.HandleError(c, err)
		return
	}

	_, err = app.db.Query("UPDATE channels SET `picture`=? WHERE `id`=?", fileName, channelID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	channel, err := app.SelectChannel(channelID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, channel)
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

func (app *App) CheckChannelAuth(channelID string, userID string) error {
	rows, err := app.db.Query("SELECT `owner` FROM channels WHERE `id`=?", channelID)
	if err != nil {
		return err
	}

	if rows.Next() {
		var owner string
		rows.Scan(&owner)

		if owner != userID {
			return &HTTPError{http.StatusForbidden, "you don't have permission on this channel"}
		}
	} else {
		return NotFoundError("channel")
	}

	return nil
}

func (app *App) ChannelAuthParam(param string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := app.CheckChannelAuth(c.Param(param), c.GetString("UserID")); err == nil {
			c.Next()
		} else {
			app.HandleError(c, err)
		}
	}
}
