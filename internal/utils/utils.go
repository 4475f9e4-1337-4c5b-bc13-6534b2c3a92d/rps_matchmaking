package utils

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

func ValidateToken(token string) (interface{}, error) {
	parsedToken, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		secret := []byte(os.Getenv("JWT_SECRET"))
		return secret, nil
	})
	if err != nil {
		return nil, err
	}

	if claims, ok := parsedToken.Claims.(jwt.MapClaims); ok && parsedToken.Valid {
		return claims, nil
	}
	return nil, errors.New("Invalid token")
}

func UnmarshalMessage[T any](data []byte, v *T) bool {
	err := json.Unmarshal(data, v)
	if err != nil {
		log.Println("Error unmarshalling message:", err)
		return false
	}
	return true
}

func ExtractPlayerID(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token, ok := c.Get("user").(*jwt.Token)
		if !ok {
			log.Println("Token not found")
			return echo.NewHTTPError(http.StatusUnauthorized, "")
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			log.Println("Claims not found")
			return echo.NewHTTPError(http.StatusUnauthorized, "")
		}

		sub, err := claims.GetSubject()
		if err != nil || sub == "" {
			log.Println("Subject not found")
			return echo.NewHTTPError(http.StatusUnauthorized, "")
		}

		c.Set("playerID", sub)
		return next(c)
	}
}
