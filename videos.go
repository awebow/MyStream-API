package main

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
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
	rows, err := app.db.Unsafe().Queryx("SELECT * FROM videos WHERE `id`=?", id)
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

func (app *App) GetVideo(c *gin.Context) {
	videoID := c.Param("id")

	video, err := app.SelectVideo(videoID)
	if err == nil {
		c.JSON(http.StatusOK, video)
	} else {
		app.HandleError(c, err)
	}
}

func (app *App) GetVideos(c *gin.Context) {
	id := c.Query("last_id")
	limit := 20

	var err error
	if q := c.Query("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil {
			app.HandleError(c, err)
			return
		}
	}

	if limit < 1 || limit > 100 {
		app.HandleError(c, &HTTPError{http.StatusBadRequest, "value of 'limit' has to be 1~100"})
		return
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
		app.HandleError(c, err)
		return
	}

	videos := []Video{}
	for rows.Next() {
		video := Video{}
		rows.StructScan(&video)

		videos = append(videos, video)
	}

	c.JSON(http.StatusOK, videos)
}

func (app *App) PostVideo(c *gin.Context) {
	body := struct {
		ChannelID   string `json:"channel_id" binding:"required"`
		Title       string `json:"title" binding:"required"`
		Description string `json:"description" binding:"required"`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	rows, err := app.db.Query("SELECT `owner` FROM channels WHERE `id`=?", body.ChannelID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows.Next() {
		var owner string
		rows.Scan(&owner)

		if owner != c.GetString("UserID") {
			app.HandleError(c, &HTTPError{http.StatusForbidden, "you don't have permission on this channel"})
			return
		}
	} else {
		app.HandleError(c, NotFoundError("channel"))
		return
	}

	sql := "INSERT INTO videos (`id`, `channel_id`, `title`, `description`, `post_started_at`) " +
		"VALUES (?, ?, ?, ?, ?)"
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

		_, err := stmt.Exec(id.String(), body.ChannelID, body.Title, body.Description, now)

		if err == nil {
			inserted = true
			break
		}
	}

	if inserted {
		token := jwt.New()
		token.Set("video_id", id)
		signed, err := jwt.Sign(token, jwa.HS256, []byte(app.Config.UploadSignKey))
		if err != nil {
			app.HandleError(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"token": string(signed)})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "Unknown server error."})
	}
}

func (app *App) PutVideo(c *gin.Context) {
	videoID := c.Param("id")
	body := struct {
		Duration *float32     `json:"duration"`
		Status   *VideoStatus `json:"status"`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	editMeta := false

	if userID := c.GetString("UserID"); userID != "" {
		sql := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
		rows, err := app.db.Query(sql, videoID)
		if err != nil {
			app.HandleError(c, AuthorizationError())
			return
		}

		if rows.Next() {
			var owner string
			rows.Scan(&owner)

			if owner != userID {
				app.HandleError(c, &HTTPError{http.StatusForbidden, "you don't have permission on this video"})
				return
			}
		}
	} else if s := strings.Split(c.GetHeader("Authorization"), " "); len(s) == 2 && s[0] == "Bearer" {
		token, err := jwt.Parse([]byte(s[1]), jwt.WithVerify(jwa.HS256, []byte(app.Config.UploadSignKey)))
		if err != nil {
			app.HandleError(c, AuthorizationError())
			return
		}

		if id, ok := token.Get("video_id"); !ok || id != videoID {
			app.HandleError(c, AuthorizationError())
			return
		}

		editMeta = token.Issuer() == "encoder"
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
	}

	if len(params) == 0 {
		app.HandleError(c, &HTTPError{http.StatusBadRequest, "no available property"})
		return
	}

	sql := "UPDATE videos" +
		" SET " + strings.Join(params, ",") +
		" WHERE `id`=?"
	_, err := app.db.Exec(sql, append(vals, videoID)...)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if video, err := app.SelectVideo(videoID); err == nil {
		c.JSON(http.StatusOK, video)
	} else {
		app.HandleError(c, err)
	}
}

func (app *App) PutThumbnail(c *gin.Context) {
	videoID := c.Param("id")

	sql := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
	rows, err := app.db.Query(sql, videoID)
	if err != nil {
		app.HandleError(c, AuthorizationError())
		return
	}

	if rows.Next() {
		var owner string
		rows.Scan(&owner)

		if owner != c.GetString("UserID") {
			app.HandleError(c, &HTTPError{http.StatusForbidden, "you don't have permission on this video"})
			return
		}
	}

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

	resized := imaging.Resize(img, app.Config.Thumbnail.Width, app.Config.Thumbnail.Height, imaging.Lanczos)
	temp, err := ioutil.TempFile("", "thumbnail")
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer os.Remove(temp.Name())

	err = imaging.Encode(temp, resized, imaging.JPEG, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if err = temp.Close(); err != nil {
		app.HandleError(c, err)
		return
	}

	if err = app.StoreFile(temp.Name(), videoID+"/thumbnail.jpg"); err != nil {
		app.HandleError(c, err)
		return
	}

	c.AbortWithStatus(http.StatusNoContent)
}

func (app *App) GetVideoComments(c *gin.Context) {
	videoID := c.Param("id")

	rows, err := app.db.Queryx("SELECT 1 FROM videos WHERE `id`=? AND `status`='ACTIVE'", videoID)
	if err != nil {
		app.HandleError(c, err)
		return
	} else if !rows.Next() {
		app.HandleError(c, NotFoundError("video"))
		return
	}

	rows, err = app.db.Unsafe().Queryx("SELECT * FROM comments WHERE `video_id`=?", videoID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	comments := []Comment{}
	for rows.Next() {
		comment := Comment{}
		rows.StructScan(&comment)
		comments = append(comments, comment)
	}

	c.JSON(http.StatusOK, comments)
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
	rows, err := app.db.Unsafe().Queryx("SELECT * FROM comments WHERE `id`=?", id)
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

func (app *App) PostComment(c *gin.Context) {
	body := struct {
		VideoID string `json:"video_id"`
		Content string `json:"content"`
	}{}
	if err := c.ShouldBindJSON(&body); err != nil {
		app.HandleError(c, err)
		return
	}

	stmt, err := app.db.Prepare(
		"INSERT INTO comments (`id`, `video_id`, `content`, `writer_id`, `posted_at`) " +
			"SELECT ?, `id`, ?, ?, ? FROM videos WHERE `id`=? AND `status`='ACTIVE'",
	)
	if err != nil {
		app.HandleError(c, err)
		return
	}
	defer stmt.Close()

	var id ulid.ULID
	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	var res sql.Result
	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		res, err = stmt.Exec(id.String(), body.Content, c.GetString("UserID"), now, body.VideoID)

		if err == nil {
			break
		}
	}
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows, err := res.RowsAffected(); err != nil {
		app.HandleError(c, err)
	} else if rows == 0 {
		app.HandleError(c, NotFoundError("video"))
	} else {
		comment, _ := app.SelectComment(id.String())
		c.JSON(http.StatusOK, comment)
	}
}

func (app *App) DeleteComment(c *gin.Context) {
	commentID := c.Param("id")
	rows, err := app.db.Query("SELECT `writer_id` FROM comments WHERE `id`=?", commentID)
	if err != nil {
		app.HandleError(c, err)
		return
	}

	if rows.Next() {
		var writer string
		rows.Scan(&writer)

		if writer != c.GetString("UserID") {
			app.HandleError(c, &HTTPError{http.StatusForbidden, "you don't have permission on this comment"})
			return
		}
	} else {
		app.HandleError(c, NotFoundError("comment"))
		return
	}

	sql := "DELETE FROM comments WHERE `id`=?"
	if _, err = app.db.Exec(sql, commentID); err == nil {
		c.AbortWithStatus(http.StatusNoContent)
	} else {
		app.HandleError(c, err)
	}
}
