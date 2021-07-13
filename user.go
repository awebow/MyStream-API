package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/sha3"
)

type User struct {
	ID            string     `json:"id" db:"id"`
	Email         string     `json:"email" db:"email"`
	Name          string     `json:"name" db:"name"`
	Picture       *string    `json:"picture" db:"picture"`
	RegisterdAt   time.Time  `json:"registered_at" db:"registered_at"`
	DeactivatedAt *time.Time `json:"deactivated_at" db:"deactivated_at"`
}

func (app *App) SelectUser(id string) (u *User, err error) {
	u = &User{}
	err = app.db.Unsafe().Get(u, "SELECT * FROM users WHERE `id`=?", id)
	if err == sql.ErrNoRows {
		err = NotFoundError("user")
	}

	return
}

func (app *App) GetUser(c echo.Context) error {
	var response struct {
		ID      string  `json:"id" db:"id"`
		Name    string  `json:"name" db:"name"`
		Picture *string `json:"picture" db:"picture"`
	}

	query := "SELECT `id`, `name`, `picture` FROM users WHERE `id`=?"
	err := app.db.Get(&response, query, c.Param("id"))
	if err == sql.ErrNoRows {
		return NotFoundError("user")
	} else if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, response)
}

func (app *App) GetMe(c echo.Context) error {
	me, err := app.SelectUser(GetUserID(c))
	if err != nil {
		if v, ok := err.(*echo.HTTPError); ok && v.Code == http.StatusNotFound {
			err = echo.ErrUnauthorized
		}

		return err
	}

	return c.JSON(http.StatusOK, me)
}

func (app *App) PutMe(c echo.Context) error {
	body := struct {
		CurrentPassword *string `json:"current_pw"`
		Password        *string `json:"password" validate:"omitempty,min=8,max=255"`
		Name            *string `json:"name" validate:"omitempty,max=64"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := c.Validate(body); err != nil {
		return err
	}

	if body.Name == nil && body.Password == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "no available field")
	}

	if body.Password != nil && body.CurrentPassword == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "the current password is needed to change password")
	}

	userID := GetUserID(c)

	var password *[]byte
	if body.Password != nil {
		var email string
		var currentPassword []byte

		query := "SELECT `email`, `password` FROM users WHERE `id`=?"
		err := app.db.QueryRow(query, userID).Scan(&email, &currentPassword)
		if err != nil {
			return err
		}

		if !bytes.Equal(currentPassword, hashPassword(email, *body.CurrentPassword)) {
			return echo.NewHTTPError(http.StatusUnauthorized, "wrong current password")
		}

		hashed := hashPassword(email, *body.Password)
		password = &hashed
	}

	sql := "UPDATE users SET `password`=IFNULL(?, `password`), `name`=IFNULL(?, `name`)" +
		"WHERE `id`=?"
	_, err := app.db.Exec(sql, password, body.Name, userID)
	if err != nil {
		return err
	}

	return c.NoContent(http.StatusNoContent)
}

func (app *App) GetEmail(c echo.Context) error {
	email := c.Param("email")
	sql := "SELECT 1 FROM users WHERE `email`=?"
	rows, err := app.db.Queryx(sql, email)
	if err != nil {
		return err
	}
	defer rows.Close()

	if rows.Next() {
		return c.JSON(http.StatusOK, echo.Map{"email": email})
	} else {
		return NotFoundError("user")
	}
}

func (app *App) GetMyChannels(c echo.Context) error {
	var response struct {
		Pagination *string   `json:"pagination"`
		Data       []Channel `json:"data"`
	}

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

	if pageToken != "" {
		query := "SELECT * FROM channels WHERE `owner`=? AND `id` < ? ORDER BY `id` DESC LIMIT ?"
		err = app.db.Unsafe().Select(&response.Data, query, GetUserID(c), pageToken, limit+1)
	} else {
		query := "SELECT * FROM channels WHERE `owner`=? ORDER BY `id` DESC LIMIT ?"
		err = app.db.Unsafe().Select(&response.Data, query, GetUserID(c), limit+1)
	}

	if err != nil {
		return err
	}

	if len(response.Data) > limit {
		response.Pagination = &response.Data[limit-1].ID
		response.Data = response.Data[:limit]
	}

	return c.JSON(http.StatusOK, response)
}

func (app *App) PostUser(c echo.Context) error {
	body := struct {
		Email    string `json:"email" validate:"email,required,max=255"`
		Password string `json:"password" validate:"required,min=8,max=255"`
		Name     string `json:"name" validate:"required,max=64"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := c.Validate(body); err != nil {
		return err
	}

	now := time.Now()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)

	var id ulid.ULID
	tx, err := app.db.Beginx()
	if err != nil {
		return err
	}

	_, err = tx.Exec("INSERT INTO users (`id`, `email`, `password`, `name`, `registered_at`) VALUES (?, ?, ?, ?, ?)",
		body.Email,
		body.Email,
		hashPassword(body.Email, body.Password),
		body.Name,
		now,
	)

	if err != nil {
		tx.Rollback()

		if v, ok := err.(*mysql.MySQLError); ok && v.Number == 1062 {
			return echo.NewHTTPError(http.StatusConflict, "the e-mail is already registered")
		} else {
			return err
		}
	}

	stmt, err := tx.Prepare("UPDATE users SET `id`=? WHERE `email`=?")
	if err != nil {
		tx.Rollback()
		return err
	}

	for i := 0; i < app.Config.ULIDConflictRetry+1; i++ {
		id = ulid.MustNew(ulid.Timestamp(now), entropy)

		_, err = stmt.Exec(id.String(), body.Email)

		if err == nil {
			break
		}
	}

	if err != nil {
		stmt.Close()
		tx.Rollback()
		return err
	}

	if err = stmt.Close(); err == nil {
		tx.Commit()
		return c.JSON(http.StatusOK, echo.Map{"id": id})
	} else {
		tx.Rollback()
		return err
	}
}

func (app *App) PutUserPicture(c echo.Context) error {
	userID := GetUserID(c)

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
	fileName := "u" + userID + ulid.MustNew(ulid.Timestamp(now), entropy).String()

	if err = app.StoreFile(dir, "images/"+fileName); err != nil {
		return err
	}

	_, err = app.db.Exec("UPDATE users SET `picture`=? WHERE `id`=?", fileName, userID)
	if err != nil {
		return err
	}

	me, err := app.SelectUser(userID)
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, me)
}

func (app *App) PostToken(c echo.Context) error {
	body := struct {
		Email    string `json:"email" validate:"required"`
		Password string `json:"password" validate:"required"`
	}{}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := c.Validate(body); err != nil {
		return err
	}

	var id string

	query := "SELECT `id` FROM users WHERE `email`=? AND `password`=?"
	err := app.db.Get(&id, query, body.Email, hashPassword(body.Email, body.Password))
	if err == sql.ErrNoRows {
		return echo.ErrUnauthorized
	} else if err != nil {
		return err
	}

	token := jwt.New()
	token.Set("user_id", id)

	signed, err := jwt.Sign(token, jwa.HS256, []byte(app.Config.AuthSignKey))
	if err != nil {
		return err
	}

	return c.JSON(http.StatusOK, echo.Map{"token": string(signed)})
}

func (app *App) AuthUser(bearer string) (string, error) {
	if s := strings.Split(bearer, " "); len(s) == 2 && s[0] == "Bearer" {
		token, err := jwt.Parse([]byte(s[1]), jwt.WithVerify(jwa.HS256, []byte(app.Config.AuthSignKey)))
		if err != nil {
			return "", err
		}

		if id, ok := token.Get("user_id"); ok {
			return id.(string), nil
		}
	}

	return "", echo.NewHTTPError(http.StatusUnauthorized, "invalid authorization token")
}

func (app *App) AuthUserMiddleware(allowUnauth bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, err := app.AuthUser(c.Request().Header.Get("Authorization"))
			if !allowUnauth && err != nil {
				return err
			}

			c.Set("UserID", id)
			return next(c)
		}
	}
}

func hashPassword(email string, password string) []byte {
	hashed := sha3.Sum256([]byte(email + password))
	return hashed[:]
}

func GetUserID(c echo.Context) (id string) {
	id, _ = c.Get("UserID").(string)
	return
}
