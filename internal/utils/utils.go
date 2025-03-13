package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
)

type GameSettings struct {
	GameMode  string `json:"gamemode"`
	Type      string `json:"game_type"`
	BestOf    int    `json:"bestOf"`
	PlayerOne string `json:"playerOne"`
	PlayerTwo string `json:"playerTwo,omitempty"`
}

type NewServerResponse struct {
	ServerID string `json:"server_id"`
}

func GetNewServerID(settings GameSettings) (string, error) {
	url := os.Getenv("GAME_SERVER_URI")
	if url == "" {
		return "", errors.New("GAME_SERVER_URI is missing")
	}
	url = url + "/game"

	settingsJson, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(settingsJson))
	if err != nil {
		return "", errors.New("Failed to create request")
	}
	token, err := CreateJWT("matchmaker")
	if err != nil {
		return "", err
	}
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("Failed to get server ID")
	}

	var newServerResponse NewServerResponse
	if ok := UnmarshalMessage(body, &newServerResponse); !ok {
		return "", errors.New("Failed to unmarshal response")
	}

	return newServerResponse.ServerID, nil
}

func CreateJWT(payload string) (string, error) {
	secret := []byte(os.Getenv("JWT_MM_SECRET"))
	now := time.Now().UTC()
	claims := make(jwt.MapClaims)
	claims["sub"] = payload
	claims["exp"] = now.Add(time.Hour * 24 * 3).Unix()
	claims["iat"] = now.Unix()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		return "", err
	}
	return token, nil
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
