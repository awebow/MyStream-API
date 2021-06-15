package main

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/disintegration/imaging"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/oklog/ulid/v2"
)

type VideoStatus int

const (
	StatusActive VideoStatus = iota
	StatusEncoding
	StatusInactive
)

func (v VideoStatus) String() string {
	switch v {
	case StatusActive:
		return "ACTIVE"
	case StatusEncoding:
		return "ENCODING"
	case StatusInactive:
		return "INACTIVE"
	}

	return ""
}

func ParseVideoStatus(s string) (VideoStatus, error) {
	switch strings.ToUpper(s) {
	case "ACTIVE":
		return StatusActive, nil
	case "ENCODING":
		return StatusEncoding, nil
	case "INACTIVE":
		return StatusInactive, nil
	}

	return 0, errors.New("invalid value for VideoStatus")
}

func (v VideoStatus) Value() (driver.Value, error) {
	return v.String(), nil
}

func (v *VideoStatus) Scan(src interface{}) (err error) {
	switch src.(type) {
	case string:
		*v, err = ParseVideoStatus(src.(string))
	case []byte:
		*v, err = ParseVideoStatus(string(src.([]byte)))
	default:
		err = errors.New("invalid type for VideoStatus")
	}
	return
}

func (v VideoStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.String())
}

func (v *VideoStatus) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}

	*v, err = ParseVideoStatus(s)
	return err
}

type Video struct {
	ID            string      `json:"id" db:"id"`
	ChannelID     string      `json:"channel_id" db:"channel_id"`
	Title         string      `json:"title" db:"title"`
	Description   string      `json:"description" db:"description"`
	Duration      float32     `json:"duration" db:"duration"`
	Status        VideoStatus `json:"status" db:"status"`
	PostedAt      *time.Time  `json:"posted_at" db:"posted_at"`
	DeactivatedAt *time.Time  `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectVideo(id string) (v *Video, err error) {
	v = &Video{}
	var rows *sqlx.Rows
	rows, err = app.db.Unsafe().Queryx("SELECT * FROM videos WHERE `id`=?", id)
	if err != nil {
		return
	}

	if rows.Next() {
		err = rows.StructScan(v)
	} else {
		err = NotFoundError("video")
	}
	return
}

func (app *App) GetVideo(c echo.Context) error {
	video, err := app.SelectVideo(c.Param("id"))
	if err != nil {
		return err
	}

	ownerID, err := app.SelectChannelOwnerID(video.ChannelID)
	if err != nil {
		return err
	}

	if video.Status == StatusInactive || (video.Status == StatusEncoding && ownerID != GetUserID(c)) {
		return NotFoundError("video")
	}

	return c.JSON(http.StatusOK, video)
}

func (app *App) GetVideos(c echo.Context) error {
	id := c.QueryParam("last_id")
	limit := 20

	var err error
	if q := c.QueryParam("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil {
			return err
		}
	}

	if limit < 1 || limit > 100 {
		return echo.NewHTTPError(http.StatusBadRequest, "value of 'limit' has to be 1~100")
	}

	var rows *sqlx.Rows
	if id == "" {
		sql := "SELECT * FROM videos WHERE `status`='ACTIVE' ORDER BY `id` DESC LIMIT ?"
		rows, err = app.db.Unsafe().Queryx(sql, limit)
	} else {
		sql := "SELECT * FROM videos WHERE `id` < ? AND `status`='ACTIVE' ORDER BY `id` DESC LIMIT ?"
		rows, err = app.db.Unsafe().Queryx(sql, id, limit)
	}

	if err != nil {
		return err
	}

	videos := []Video{}
	for rows.Next() {
		video := Video{}
		rows.StructScan(&video)

		videos = append(videos, video)
	}

	return c.JSON(http.StatusOK, videos)
}

func (app *App) PostVideo(c echo.Context) error {
	body := struct {
		ChannelID   string `json:"channel_id" validate:"required"`
		Title       string `json:"title" validate:"required"`
		Description string `json:"description" validate:"required"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := c.Validate(&body); err != nil {
		return err
	}

	if err := app.CheckChannelAuth(body.ChannelID, GetUserID(c)); err != nil {
		return err
	}

	sql := "INSERT INTO videos (`id`, `channel_id`, `title`, `description`, `post_started_at`) " +
		"VALUES (?, ?, ?, ?, ?)"
	stmt, err := app.db.Prepare(sql)
	if err != nil {
		return err
	}
	defer stmt.Close()

	var id ulid.ULID
	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err = stmt.Exec(id.String(), body.ChannelID, body.Title, body.Description, now)

		if err == nil {
			break
		}
	}

	if err == nil {
		token := jwt.New()
		token.Set("video_id", id)
		signed, err := jwt.Sign(token, jwa.HS256, []byte(app.Config.UploadSignKey))
		if err != nil {
			return err
		}

		return c.JSON(http.StatusOK, &struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		}{id.String(), string(signed)})
	} else {
		return err
	}
}

func (app *App) PutVideo(c echo.Context) error {
	videoID := c.Param("id")
	body := struct {
		Duration *float32     `json:"duration"`
		Status   *VideoStatus `json:"status"`
		PostedAt *time.Time   `json:"posted_at"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}

	editMeta := false

	if userID := GetUserID(c); userID != "" {
		sql := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
		rows, err := app.db.Query(sql, videoID)
		if err != nil {
			return echo.ErrUnauthorized
		}

		if rows.Next() {
			var owner string
			rows.Scan(&owner)

			if owner != userID {
				return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this video")
			}
		}
	} else if token, ok := c.Get("uploadToken").(*jwtgo.Token); ok {
		claims := token.Claims.(jwtgo.MapClaims)
		if id, ok := claims["video_id"].(string); !ok || id != videoID {
			return echo.ErrUnauthorized
		}

		editMeta = claims["iss"] == "encoder"
	} else {
		return echo.ErrUnauthorized
	}

	params := []string{}
	vals := []interface{}{}

	if editMeta {
		if body.Duration != nil {
			params = append(params, "`duration`=?")
			vals = append(vals, *body.Duration)
		}

		if body.Status != nil {
			params = append(params, "`status`=?")
			vals = append(vals, *body.Status)
		}

		if body.PostedAt != nil {
			params = append(params, "`posted_at`=?")
			vals = append(vals, *body.PostedAt)
		}
	}

	if len(params) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no available property")
	}

	sql := "UPDATE videos" +
		" SET " + strings.Join(params, ",") +
		" WHERE `id`=?"
	_, err := app.db.Exec(sql, append(vals, videoID)...)
	if err != nil {
		return err
	}

	if video, err := app.SelectVideo(videoID); err == nil {
		if editMeta && body.Status != nil && *body.Status == StatusActive {
			room := fmt.Sprintf("video/%s/encode", videoID)
			app.ws.Publish(room, "encoded", video)
			app.ws.UnsubscribeAll(room)
		}

		return c.JSON(http.StatusOK, video)
	} else {
		return err
	}
}

func (app *App) PutThumbnail(c echo.Context) error {
	videoID := c.Param("id")

	sql := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
	rows, err := app.db.Query(sql, videoID)
	if err != nil {
		return echo.ErrUnauthorized
	}

	if rows.Next() {
		var owner string
		rows.Scan(&owner)

		if owner != GetUserID(c) {
			return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this video")
		}
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

	resized := imaging.Resize(img, app.Config.Thumbnail.Width, app.Config.Thumbnail.Height, imaging.Lanczos)
	temp, err := ioutil.TempFile("", "thumbnail")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	err = imaging.Encode(temp, resized, imaging.JPEG, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
	if err != nil {
		temp.Close()
		return err
	}

	if err = temp.Close(); err != nil {
		return err
	}

	if err = app.StoreFile(temp.Name(), "videos/"+videoID+"/thumbnail.jpg"); err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

func (app *App) GetVideoComments(c echo.Context) error {
	videoID := c.Param("id")

	sql := "SELECT 1 FROM videos v JOIN channels c ON c.`id`=v.`channel_id` " +
		"WHERE v.`id`=? AND (v.`status`='ACTIVE' OR (v.`status`='ENCODING' AND c.`owner`=?))"
	rows, err := app.db.Queryx(sql, videoID, GetUserID(c))
	if err != nil {
		return err
	} else if !rows.Next() {
		return NotFoundError("video")
	}

	rows, err = app.db.Unsafe().Queryx("SELECT * FROM comments WHERE `video_id`=?", videoID)
	if err != nil {
		return err
	}

	comments := []Comment{}
	for rows.Next() {
		comment := Comment{}
		rows.StructScan(&comment)
		comments = append(comments, comment)
	}

	return c.JSON(http.StatusOK, comments)
}

type Comment struct {
	ID            string     `json:"id" db:"id"`
	VideoID       string     `json:"video_id" db:"video_id"`
	Content       string     `json:"content" db:"content"`
	WriterID      string     `json:"writer_id" db:"writer_id"`
	PostedAt      time.Time  `json:"posted_at" db:"posted_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectComment(id string) (c *Comment, err error) {
	c = &Comment{}
	var rows *sqlx.Rows
	rows, err = app.db.Unsafe().Queryx("SELECT * FROM comments WHERE `id`=?", id)
	if err != nil {
		return
	}

	if rows.Next() {
		err = rows.StructScan(c)
	} else {
		err = NotFoundError("comment")
	}
	return
}

func (app *App) PostComment(c echo.Context) error {
	body := struct {
		VideoID string `json:"video_id"`
		Content string `json:"content"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}

	stmt, err := app.db.Prepare(
		"INSERT INTO comments (`id`, `video_id`, `content`, `writer_id`, `posted_at`) " +
			"SELECT ?, v.`id`, ?, ?, ? FROM videos v JOIN channels c ON c.`id`=v.`channel_id` " +
			"WHERE v.`id`=? AND (v.`status`='ACTIVE' OR (v.`status`='ENCODING' AND c.`owner`=?))",
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	userId := GetUserID(c)
	var id ulid.ULID
	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	var res sql.Result
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		res, err = stmt.Exec(id.String(), body.Content, userId, now, body.VideoID, userId)

		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}

	if rows, err := res.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return NotFoundError("video")
	} else {
		comment, _ := app.SelectComment(id.String())
		return c.JSON(http.StatusOK, comment)
	}
}

func (app *App) DeleteComment(c echo.Context) error {
	commentID := c.Param("id")
	rows, err := app.db.Query("SELECT `writer_id` FROM comments WHERE `id`=?", commentID)
	if err != nil {
		return err
	}

	if rows.Next() {
		var writer string
		rows.Scan(&writer)

		if writer != GetUserID(c) {
			return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this comment")
		}
	} else {
		return NotFoundError("comment")
	}

	sql := "DELETE FROM comments WHERE `id`=?"
	if _, err = app.db.Exec(sql, commentID); err == nil {
		return c.NoContent(http.StatusNoContent)
	} else {
		return err
	}
}
