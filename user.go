package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/disintegration/imaging"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
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
	var rows *sqlx.Rows
	rows, err = app.db.Unsafe().Queryx("SELECT * FROM users WHERE `id`=?", id)
	if err != nil {
		return
	}

	if rows.Next() {
		err = rows.StructScan(u)
	} else {
		err = NotFoundError("user")
	}
	return
}

func (app *App) GetUser(c echo.Context) error {
	sql := "SELECT `id`, `name`, `picture` FROM users WHERE `id`=?"
	rows, err := app.db.Queryx(sql, c.Param("id"))
	if err != nil {
		return err
	}

	if rows.Next() {
		blob, err := RowToJSON(rows)
		if err != nil {
			return nil
		}

		return c.JSONBlob(http.StatusOK, blob)
	} else {
		return NotFoundError("user")
	}
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
		rows, err := app.db.Query("SELECT `email`, `password` FROM users WHERE `id`=?", userID)
		if err != nil {
			return nil
		}

		var email string
		var currentPassword []byte
		rows.Next()
		rows.Scan(&email, &currentPassword)

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

	if rows.Next() {
		return c.JSON(http.StatusOK, echo.Map{"email": email})
	} else {
		return NotFoundError("user")
	}
}

func (app *App) GetMyChannels(c echo.Context) error {
	rows, err := app.db.Unsafe().Queryx("SELECT * FROM channels WHERE `owner`=?", GetUserID(c))
	if err != nil {
		return err
	}

	channels := []Channel{}
	for rows.Next() {
		channel := Channel{}
		rows.StructScan(&channel)
		channels = append(channels, channel)
	}

	return c.JSON(http.StatusOK, channels)
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

	_, err = app.db.Query("UPDATE users SET `picture`=? WHERE `id`=?", fileName, userID)
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

	sql := "SELECT `id` FROM users WHERE `email`=? AND `password`=?"
	rows, err := app.db.Query(sql, body.Email, hashPassword(body.Email, body.Password))
	if err != nil {
		return err
	}

	if rows.Next() {
		var id string
		rows.Scan(&id)

		token := jwt.New()
		token.Set("user_id", id)

		signed, err := jwt.Sign(token, jwa.HS256, []byte(app.Config.AuthSignKey))
		if err != nil {
			return err
		}

		return c.JSON(http.StatusOK, echo.Map{"token": string(signed)})
	} else {
		return echo.ErrUnauthorized
	}
}

func hashPassword(email string, password string) []byte {
	hashed := sha3.Sum256([]byte(email + password))
	return hashed[:]
}

func GetUserID(c echo.Context) string {
	if token, ok := c.Get("user").(*jwtgo.Token); ok {
		return token.Claims.(jwtgo.MapClaims)["user_id"].(string)
	}

	return ""
}
