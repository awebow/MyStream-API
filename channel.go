package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/disintegration/imaging"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/oklog/ulid/v2"
	"github.com/olivere/elastic/v7"
)

type Channel struct {
	ID            string     `json:"id" db:"id"`
	Name          string     `json:"name" db:"name"`
	Description   string     `json:"description" db:"description"`
	Picture       *string    `json:"picture" db:"picture"`
	Subscribers   uint64     `json:"subscribers" db:"subscribers"`
	Videos        uint64     `json:"videos" db:"videos"`
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
	defer rows.Close()

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

func (app *App) GetChannels(c echo.Context) error {
	response := struct {
		Pagination *string   `json:"pagination"`
		Data       []Channel `json:"data"`
	}{Data: []Channel{}}

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
			Index(app.Config.Elasticsearch.ChannelIndex)

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
			elastic.NewMultiMatchQuery(q, "name^2", "description"),
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

		stmt, err := app.db.Unsafe().Preparex("SELECT * FROM channels WHERE `id`=?")
		if err != nil {
			return err
		}

		response.Data = make([]Channel, len(res.Hits.Hits))
		for i, hit := range res.Hits.Hits {
			stmt.Get(&response.Data[i], hit.Id)
		}
	} else {
		if pageToken != "" {
			query := "SELECT * FROM channels WHERE `id` < ? ORDER BY `id` DESC LIMIT ?"
			err = app.db.Unsafe().Select(&response.Data, query, pageToken, limit+1)
		} else {
			query := "SELECT * FROM channels ORDER BY `id` DESC LIMIT ?"
			err = app.db.Unsafe().Select(&response.Data, query, limit+1)
		}

		if err != nil {
			return err
		}

		if length := len(response.Data); length == limit+1 {
			response.Pagination = &response.Data[length-2].ID
		}
	}

	if len(response.Data) > limit {
		response.Data = response.Data[:limit]
	}
	return c.JSON(http.StatusOK, response)
}

func (app *App) GetSubscribedChannels(c echo.Context) error {
	var response struct {
		Pagination *string   `json:"pagination"`
		Data       []Channel `json:"data"`
	}

	pageToken := c.QueryParam("pagination")
	limit := 20
	userId := GetUserID(c)

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

	if pageToken != "" {
		query :=
			`SELECT c.* FROM channels c JOIN subscriptions s ON c.id=s.channel_id
				WHERE s.user_id=? AND c.id < ?
				ORDER BY c.id DESC LIMIT ?`
		err = app.db.Unsafe().Select(&response.Data, query, userId, pageToken, limit+1)
	} else {
		query :=
			`SELECT c.* FROM channels c JOIN subscriptions s ON c.id=s.channel_id
				WHERE s.user_id=?
				ORDER BY id DESC LIMIT ?`
		err = app.db.Unsafe().Select(&response.Data, query, userId, limit+1)
	}

	if err != nil {
		return err
	}

	if length := len(response.Data); length == limit+1 {
		response.Pagination = &response.Data[length-2].ID
	}

	if len(response.Data) > limit {
		response.Data = response.Data[:limit]
	}
	return c.JSON(http.StatusOK, response)
}

func (app *App) GetChannelPermission(c echo.Context) error {
	rows, err := app.db.Query("SELECT 1 FROM channels WHERE `owner`=?", GetUserID(c))
	if err != nil {
		return err
	}
	defer rows.Close()

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

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	sql := "INSERT INTO channels " +
		"(`id`, `name`, `description`, `owner`, `created_at`, `updated_at`) VALUES (?, ?, ?, ?, ?, ?)"
	stmt, err := tx.Prepare(sql)
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

		_, err = stmt.Exec(id.String(), body.Name, body.Description, GetUserID(c), now, now)

		if err == nil {
			inserted = true
			break
		}
	}

	if inserted {
		_, err = app.es.Index().
			Index(app.Config.Elasticsearch.ChannelIndex).
			Id(id.String()).
			BodyJson(echo.Map{
				"name":        body.Name,
				"description": body.Description,
				"updated_at":  now,
			}).
			Do(context.Background())

		if err == nil {
			if err = tx.Commit(); err != nil {
				return err
			}

			return c.JSON(http.StatusOK, echo.Map{"id": id})
		}
	}

	tx.Rollback()
	return err
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

	_, err = app.db.Exec("UPDATE channels SET `picture`=? WHERE `id`=?", fileName, channelID)
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

func (app *App) GetSubscription(c echo.Context) error {
	var response struct {
		Subscribed  bool   `json:"subscribed"`
		Subscribers uint64 `json:"subscribers"`
	}

	userId := GetUserID(c)
	channelId := c.Param("id")

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	query := "SELECT `subscribers` FROM channels WHERE `id`=?"
	err = tx.Get(&response.Subscribers, query, channelId)
	if err != nil {
		return err
	}

	query = "SELECT 1 FROM subscriptions WHERE `user_id`=? AND `channel_id`=?"
	rows, err := tx.Query(query, userId, channelId)
	if err != nil {
		tx.Rollback()
		return err
	}
	response.Subscribed = rows.Next()
	rows.Close()

	tx.Commit()
	return c.JSON(http.StatusOK, response)
}

func (app *App) PostSubscription(c echo.Context) error {
	var response struct {
		Subscribed  bool   `json:"subscribed"`
		Subscribers uint64 `json:"subscribers"`
	}

	userId := GetUserID(c)
	channelId := c.Param("id")

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	if _, err = tx.Exec("INSERT INTO subscriptions VALUES (?, ?)", userId, channelId); err != nil {
		tx.Rollback()

		if v, ok := err.(*mysql.MySQLError); ok && v.Number == 1062 {
			return echo.NewHTTPError(http.StatusConflict, "already subscribed")
		} else if ok && (v.Number == 1216 || v.Number == 1452 || v.Number == 1406) {
			return NotFoundError("channel")
		} else {
			return err
		}
	}

	query := "UPDATE channels SET `subscribers`=`subscribers`+1 WHERE `id`=?"
	if _, err = tx.Exec(query, channelId); err != nil {
		tx.Rollback()
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	response.Subscribed = true
	query = "SELECT `subscribers` FROM channels WHERE `id`=?"
	if err = app.db.Get(&response.Subscribers, query, channelId); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, response)
}

func (app *App) DeleteSubscription(c echo.Context) error {
	var response struct {
		Subscribed  bool   `json:"subscribed"`
		Subscribers uint64 `json:"subscribers"`
	}

	userId := GetUserID(c)
	channelId := c.Param("id")

	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	query := "DELETE FROM subscriptions WHERE `user_id`=? AND `channel_id`=?"
	if res, err := tx.Exec(query, userId, channelId); err != nil {
		tx.Rollback()
		return err
	} else if rows, err := res.RowsAffected(); err != nil {
		tx.Rollback()
		return err
	} else if rows == 0 {
		tx.Rollback()
		return NotFoundError("subscription")
	}

	query = "UPDATE channels SET `subscribers`=`subscribers`-1 WHERE `id`=?"
	if _, err = tx.Exec(query, channelId); err != nil {
		tx.Rollback()
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	response.Subscribed = false
	query = "SELECT `subscribers` FROM channels WHERE `id`=?"
	if err = app.db.Get(&response.Subscribers, query, channelId); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, response)
}

func (app *App) SelectChannelOwnerID(channelID string) (string, error) {
	var ownerID string
	err := app.db.Get(&ownerID, "SELECT `owner` FROM channels WHERE `id`=?", channelID)
	if err == sql.ErrNoRows {
		return "", NotFoundError("channel")
	}

	return ownerID, err
}

func (app *App) CheckChannelAuth(channelID string, userID string) error {
	ownerID, err := app.SelectChannelOwnerID(channelID)
	if err != nil {
		return err
	}

	if ownerID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "you don't have permission on this channel")
	}

	return nil
}
