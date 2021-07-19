package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/disintegration/imaging"
	"github.com/labstack/echo/v4"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/oklog/ulid/v2"
	"github.com/olivere/elastic/v7"
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
	Likes         uint64      `json:"likes" db:"likes"`
	Dislikes      uint64      `json:"dislikes" db:"dislikes"`
	PostedAt      *time.Time  `json:"posted_at" db:"posted_at"`
	UpdatedAt     time.Time   `json:"updated_at" db:"updated_at"`
	DeactivatedAt *time.Time  `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectVideo(id string) (v *Video, err error) {
	v = &Video{}
	err = app.db.Unsafe().Get(v, "SELECT * FROM videos WHERE `id`=?", id)
	if err == sql.ErrNoRows {
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
	response := struct {
		Pagination *string `json:"pagination"`
		Data       []Video `json:"data"`
	}{Data: []Video{}}

	pageToken := c.QueryParam("pagination")
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

	if q := c.QueryParam("query"); q != "" {
		search := app.es.Search().
			Index(app.Config.Elasticsearch.VideoIndex)

		searchTime := time.Now()

		if pageToken != "" {
			page, err := parsePagination(pageToken)
			if err != nil {
				return err
			}

			searchTime = page.searchTime
			search.SearchAfter(page.score, page.id.String())
		}

		search.Query(elastic.NewBoolQuery().Must(
			elastic.NewRangeQuery("updated_at").Lte(searchTime),
			elastic.NewMultiMatchQuery(q, "title^2", "description"),
		)).
			Size(limit+1).
			Sort("_score", false).
			Sort("_id", false)

		res, err := search.Do(context.Background())

		if err != nil {
			return err
		}

		if res.Hits.TotalHits.Value == 0 {
			return c.JSON(http.StatusOK, response)
		}

		if length := len(res.Hits.Hits); length == limit+1 {
			last := res.Hits.Hits[length-2]
			next := (&pagination{searchTime, *last.Score, ulid.MustParse(last.Id)}).tokenize()
			response.Pagination = &next
		}

		stmt, err := app.db.Unsafe().Preparex("SELECT * FROM videos WHERE `id`=?")
		if err != nil {
			return err
		}

		response.Data = make([]Video, len(res.Hits.Hits))
		for i, hit := range res.Hits.Hits {
			if i < limit {
				stmt.Get(&response.Data[i], hit.Id)
			}
		}
	} else {
		userId := GetUserID(c)
		if userId != "" {
			var rows []struct {
				Video
				Subscription bool `db:"subscription"`
			}

			var lastIds []string
			if pageToken == "" {
				lastIds = []string{"", ""}
			} else {
				lastIds = strings.Split(pageToken, ",")
				if len(lastIds) != 2 {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid pagination token")
				}
			}

			err = app.db.Unsafe().Select(&rows,
				`SELECT * FROM
					((SELECT v.*, UNIX_TIMESTAMP(posted_at)+? score, 1 subscription
						FROM videos v JOIN subscriptions s ON v.channel_id=s.channel_id
						WHERE
							s.user_id=?
							AND (?='' OR v.id < ?)
							AND v.status='ACTIVE'
						ORDER BY v.id DESC
						LIMIT ?)
					UNION
					(SELECT v.*, UNIX_TIMESTAMP(posted_at) score, 0 subscription
						FROM videos v LEFT JOIN subscriptions s ON v.channel_id=s.channel_id AND s.user_id=?
						WHERE s.user_id IS NULL AND v.status='ACTIVE'
						AND (?='' OR v.id < ?)
						ORDER BY v.id DESC LIMIT ?)) a
				GROUP BY id
				ORDER BY score DESC
				LIMIT ?;`,
				app.Config.SubscriptionBonus,
				userId, lastIds[0], lastIds[0], limit+1,
				userId, lastIds[1], lastIds[1], limit+1,
				limit+1,
			)

			if err != nil {
				return err
			}

			lastSub := ""
			lastRecent := ""
			for i, row := range rows {
				if i < limit {
					if row.Subscription {
						lastSub = row.ID
					} else {
						lastRecent = row.ID
					}

					response.Data = append(response.Data, row.Video)
				}
			}

			if lastSub == "" {
				lastSub = lastIds[0]
			}

			if lastRecent == "" {
				lastRecent = lastIds[1]
			}

			if len(rows) == limit+1 {
				response.Pagination = new(string)
				*response.Pagination = lastSub + "," + lastRecent
			}
		} else {
			if pageToken != "" {
				query := "SELECT * FROM videos WHERE `id` < ? ORDER BY `id` DESC LIMIT ?"
				err = app.db.Unsafe().Select(&response.Data, query, pageToken, limit+1)
			} else {
				query := "SELECT * FROM videos ORDER BY `id` DESC LIMIT ?"
				err = app.db.Unsafe().Select(&response.Data, query, limit+1)
			}

			if err != nil {
				return err
			}

			if len(response.Data) == limit+1 {
				response.Pagination = &response.Data[limit-1].ID
				response.Data = response.Data[:limit]
			}
		}
	}

	return c.JSON(http.StatusOK, response)
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

	userId := GetUserID(c)
	if err := app.CheckChannelAuth(body.ChannelID, userId); err != nil {
		return err
	}

	now := time.Now()
	id := ulid.MustNew(ulid.Timestamp(now), app.ulidEntropy)

	query := "INSERT INTO videos (`id`, `channel_id`, `title`, `description`, `post_started_at`) " +
		"VALUES (?, ?, ?, ?, ?)"
	_, err := app.db.Exec(query, id.String(), body.ChannelID, body.Title, body.Description, now)
	if err != nil {
		return err
	}

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
}

func (app *App) PutVideo(c echo.Context) error {
	videoID := c.Param("id")
	body := struct {
		Title       *string      `json:"title"`
		Description *string      `json:"description"`
		Duration    *float32     `json:"duration"`
		Status      *VideoStatus `json:"status"`
		PostedAt    *time.Time   `json:"posted_at"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}

	editMeta := false

	if userID := GetUserID(c); userID != "" {
		var owner string

		query := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
		err := app.db.Get(&owner, query, videoID)
		if err == sql.ErrNoRows {
			return NotFoundError("video")
		} else if err != nil {
			return err
		}

		if owner != userID {
			return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this video")
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

	if body.Title != nil {
		params = append(params, "`title`=?")
		vals = append(vals, *body.Title)
	}

	if body.Description != nil {
		params = append(params, "`description`=?")
		vals = append(vals, *body.Description)
	}

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

	query := "UPDATE videos" +
		" SET " + strings.Join(params, ",") + ", `updated_at`=CURRENT_TIMESTAMP()" +
		" WHERE `id`=?"
	res, err := app.db.Exec(query, append(vals, videoID)...)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return NotFoundError("video")
	}

	if video, err := app.SelectVideo(videoID); err == nil {
		if editMeta || body.Title != nil || body.Description != nil {
			app.es.Index().
				Index(app.Config.Elasticsearch.VideoIndex).
				Id(videoID).
				BodyJson(echo.Map{
					"title":       video.Title,
					"description": video.Description,
					"updated_at":  video.UpdatedAt,
				}).
				Do(context.Background())
		}

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

	var owner string

	query := "SELECT c.`owner` FROM videos v JOIN channels c ON v.`channel_id`=c.`id` WHERE v.`id`=?"
	if err := app.db.Get(&owner, query, videoID); err == sql.ErrNoRows {
		return NotFoundError("video")
	} else if err != nil {
		return err
	}

	if owner != GetUserID(c) {
		return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this video")
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

	if err = app.videoStorage.storeFile(temp.Name(), videoID+"/thumbnail.jpg"); err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

func (app *App) GetVideoComments(c echo.Context) error {
	var response struct {
		Pagination *string   `json:"pagination"`
		Data       []Comment `json:"data"`
	}

	videoID := c.Param("id")

	pageToken := c.QueryParam("pagination")
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

	sql := "SELECT 1 FROM videos v JOIN channels c ON c.`id`=v.`channel_id` " +
		"WHERE v.`id`=? AND (v.`status`='ACTIVE' OR (v.`status`='ENCODING' AND c.`owner`=?))"
	rows, err := app.db.Queryx(sql, videoID, GetUserID(c))
	if err != nil {
		return err
	} else if !rows.Next() {
		return NotFoundError("video")
	}
	rows.Close()

	if pageToken == "" {
		query := "SELECT * FROM comments WHERE `video_id`=? ORDER BY `id` DESC LIMIT ?"
		err = app.db.Unsafe().Select(&response.Data, query, videoID, limit+1)
	} else {
		query := "SELECT * FROM comments WHERE `video_id`=? AND `id` < ? ORDER BY `id` DESC LIMIT ?"
		err = app.db.Unsafe().Select(&response.Data, query, videoID, pageToken, limit+1)
	}
	if err != nil {
		return err
	}

	if len(response.Data) == limit+1 {
		response.Pagination = &response.Data[limit-1].ID
		response.Data = response.Data[:limit]
	}

	return c.JSON(http.StatusOK, response)
}

type Expression int

const (
	ExpressionLike Expression = iota
	ExpressionDislike
)

func (e Expression) String() string {
	switch e {
	case ExpressionLike:
		return "LIKE"
	case ExpressionDislike:
		return "DISLIKE"
	}

	return ""
}

func parseExpression(s string) (Expression, error) {
	switch strings.ToUpper(s) {
	case "LIKE":
		return ExpressionLike, nil
	case "DISLIKE":
		return ExpressionDislike, nil
	}

	return 0, errors.New("invalid value for Expression")
}

func (e Expression) Value() (driver.Value, error) {
	return e.String(), nil
}

func (e *Expression) Scan(src interface{}) (err error) {
	switch src.(type) {
	case string:
		*e, err = parseExpression(src.(string))
	case []byte:
		*e, err = parseExpression(string(src.([]byte)))
	default:
		err = errors.New("invalid type for Expression")
	}
	return
}

func (e Expression) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

func (e *Expression) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}

	*e, err = parseExpression(s)
	return err
}

type ExpressionInfo struct {
	MyExpression *Expression `json:"my_expression"`
	Likes        uint64      `json:"likes"`
	Dislikes     uint64      `json:"dislikes"`
}

func (app *App) GetExpression(c echo.Context) error {
	var response ExpressionInfo
	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	videoId := c.Param("id")
	userId := GetUserID(c)

	query := "SELECT `likes`, `dislikes` FROM videos WHERE `id`=? FOR UPDATE"
	if err = tx.QueryRow(query, videoId).Scan(&response.Likes, &response.Dislikes); err != nil {
		tx.Rollback()

		if err == sql.ErrNoRows {
			return NotFoundError("video")
		} else {
			return err
		}
	}

	if userId != "" {
		query = "SELECT `type` abc FROM expressions WHERE `video_id`=? AND `user_id`=? FOR UPDATE"
		err = tx.Get(&response.MyExpression, query, videoId, userId)
		if err != nil && err != sql.ErrNoRows {
			tx.Rollback()
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, response)
}

func (app *App) PutExpression(c echo.Context) error {
	var body struct {
		Type Expression `json:"type"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}

	var response ExpressionInfo

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	videoId := c.Param("id")
	userId := GetUserID(c)

	query := "SELECT `likes`, `dislikes` FROM videos WHERE `id`=? FOR UPDATE"
	if err = tx.QueryRow(query, videoId).Scan(&response.Likes, &response.Dislikes); err != nil {
		tx.Rollback()

		if err == sql.ErrNoRows {
			return NotFoundError("video")
		} else {
			return err
		}
	}

	var exp Expression
	query = "SELECT `type` FROM expressions WHERE `video_id`=? AND `user_id`=? FOR UPDATE"
	if err = tx.Get(&exp, query, videoId, userId); err == nil {
		if exp == body.Type {
			tx.Commit()
			response.MyExpression = &exp
			return c.JSON(http.StatusOK, response)
		} else if exp == ExpressionLike {
			response.Likes--
		} else {
			response.Dislikes--
		}
	} else if err != sql.ErrNoRows {
		tx.Rollback()
		return err
	}

	query = "INSERT INTO expressions VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `type`=?"
	if _, err = tx.Exec(query, videoId, userId, body.Type, body.Type); err != nil {
		tx.Rollback()
		return err
	}

	if body.Type == ExpressionLike {
		response.Likes++
	} else {
		response.Dislikes++
	}

	query = "UPDATE videos SET `likes`=?, `dislikes`=? WHERE `id`=?"
	if _, err = tx.Exec(query, response.Likes, response.Dislikes, videoId); err != nil {
		tx.Rollback()
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	response.MyExpression = &body.Type
	return c.JSON(http.StatusOK, response)
}

func (app *App) DeleteExpression(c echo.Context) error {
	var response ExpressionInfo

	videoId := c.Param("id")
	userId := GetUserID(c)

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	query := "SELECT `likes`, `dislikes` FROM videos WHERE `id`=? FOR UPDATE"
	if err = tx.QueryRow(query, videoId).Scan(&response.Likes, &response.Dislikes); err != nil {
		tx.Rollback()

		if err == sql.ErrNoRows {
			return NotFoundError("video")
		} else {
			return err
		}
	}

	var exp Expression
	query = "SELECT `type` FROM expressions WHERE `video_id`=? AND `user_id`=? FOR UPDATE"
	if err = tx.Get(&exp, query, videoId, userId); err != nil {
		tx.Rollback()

		if err == sql.ErrNoRows {
			return NotFoundError("expression")
		} else {
			return err
		}
	}

	query = "DELETE FROM expressions WHERE `video_id`=? AND `user_id`=?"
	if _, err = tx.Exec(query, videoId, userId); err != nil {
		tx.Rollback()
		return err
	}

	if exp == ExpressionLike {
		response.Likes--
	} else {
		response.Dislikes--
	}

	query = "UPDATE videos SET `likes`=?, `dislikes`=? WHERE `id`=?"
	if _, err = tx.Exec(query, response.Likes, response.Dislikes, videoId); err != nil {
		tx.Rollback()
		return err
	}

	tx.Commit()
	return c.JSON(http.StatusOK, response)
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
	err = app.db.Unsafe().Get(c, "SELECT * FROM comments WHERE `id`=?", id)
	if err == sql.ErrNoRows {
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

	query := "INSERT INTO comments (`id`, `video_id`, `content`, `writer_id`, `posted_at`) " +
		"SELECT ?, v.`id`, ?, ?, ? FROM videos v JOIN channels c ON c.`id`=v.`channel_id` " +
		"WHERE v.`id`=? AND (v.`status`='ACTIVE' OR (v.`status`='ENCODING' AND c.`owner`=?))"

	now := time.Now()
	id := ulid.MustNew(ulid.Timestamp(now), app.ulidEntropy)

	userId := GetUserID(c)
	res, err := app.db.Exec(query, id.String(), body.Content, userId, now, body.VideoID, userId)
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

	var writer string
	err := app.db.Get(&writer, "SELECT `writer_id` FROM comments WHERE `id`=?", commentID)
	if err == sql.ErrNoRows {
		return NotFoundError("comment")
	}
	if err != nil {
		return err
	}

	if writer != GetUserID(c) {
		return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this comment")
	}

	sql := "DELETE FROM comments WHERE `id`=?"
	if _, err = app.db.Exec(sql, commentID); err == nil {
		return c.NoContent(http.StatusNoContent)
	} else {
		return err
	}
}
