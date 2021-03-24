package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/disintegration/imaging"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
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

func (app *App) GetChannel(c echo.Context) error {
	channel, err := app.SelectChannel(c.Param("id"))
	if err != nil {
		return err
	} else {
		return c.JSON(http.StatusOK, channel)
	}
}

func (app *App) GetChannelPermission(c echo.Context) error {
	rows, err := app.db.Query("SELECT 1 FROM channels WHERE `owner`=?", GetUserID(c))
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, echo.Map{"ownership": rows.Next()})
}

func (app *App) PostChannel(c echo.Context) error {
	body := struct {
		Name        string `json:"name" validate:"required,max=100"`
		Description string `json:"description"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := c.Validate(body); err != nil {
		return err
	}

	sql := "INSERT INTO channels (`id`, `name`, `description`, `owner`, `created_at`) VALUES (?, ?, ?, ?, ?)"
	stmt, err := app.db.Prepare(sql)
	if err != nil {
		return err
	}
	defer stmt.Close()

	var id ulid.ULID
	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	inserted := false
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err := stmt.Exec(id.String(), body.Name, body.Description, GetUserID(c), now)

		if err == nil {
			inserted = true
			break
		}
	}

	if inserted {
		return c.JSON(http.StatusOK, echo.Map{"id": id})
	} else {
		return echo.ErrInternalServerError
	}
}

func (app *App) PutChannelPicture(c echo.Context) error {
	channelID := c.Param("id")
	if err := app.CheckChannelAuth(channelID, GetUserID(c)); err != nil {
		return err
	}

	header, err := c.FormFile("file")
	if err != nil {
		return err
	}

	file, err := header.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	img, err := imaging.Decode(file)
	if err != nil {
		return err
	}

	dir, err := ioutil.TempDir("", "picture")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	for _, o := range app.Config.UserPicture {
		output, err := os.Create(fmt.Sprintf("%s/%dx%d.jpg", dir, o.Width, o.Height))
		if err != nil {
			return err
		}

		resized := imaging.Fill(img, o.Width, o.Height, imaging.Center, imaging.Lanczos)
		err = imaging.Encode(output, resized, imaging.JPEG, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
		if err != nil {
			output.Close()
			return err
		}

		if err = output.Close(); err != nil {
			return err
		}
	}

	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	fileName := "c" + channelID + ulid.MustNew(ulid.Timestamp(now), entropy).String()

	if err = app.StoreFile(dir, "images/"+fileName); err != nil {
		return err
	}

	_, err = app.db.Query("UPDATE channels SET `picture`=? WHERE `id`=?", fileName, channelID)
	if err != nil {
		return err
	}

	channel, err := app.SelectChannel(channelID)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, channel)
}

func (app *App) GetChannelVideos(c echo.Context) error {
	channelID := c.Param("id")

	sql := "SELECT * FROM videos WHERE `channel_id`=? AND `status`='ACTIVE'"
	rows, err := app.db.Unsafe().Queryx(sql, channelID)
	if err != nil {
		return err
	}

	videos := []Video{}
	for rows.Next() {
		v := Video{}
		err = rows.StructScan(&v)
		if err != nil {
			return err
		}

		videos = append(videos, v)
	}

	return c.JSON(http.StatusOK, videos)
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
			return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this channel")
		}
	} else {
		return NotFoundError("channel")
	}

	return nil
}
