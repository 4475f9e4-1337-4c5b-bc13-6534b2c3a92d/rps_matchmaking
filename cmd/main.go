package main

import (
	"log"
	"net/http"
	"os"
	"rps_matchmaking/internal/matchmaking"
	"rps_matchmaking/internal/utils"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var queue = matchmaking.NewMatchmakingQueue()

func JoinQueue(c echo.Context) error {
	id, ok := c.Get("playerID").(string)
	if !ok {
		return c.NoContent(http.StatusUnauthorized)
	}

	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Println("ws upgrade err", err)
		return c.NoContent(http.StatusBadRequest)
	}

	log.Println("Player", id, "joined queue")
	queue.Enqueue(id, conn)
	return nil
}

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	go queue.Tick()
}

func main() {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is missing")
	}
	e := echo.New()
	e.HideBanner = true
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("Authorization") == "" {
				cookie, err := c.Cookie("access_token")
				if err == nil {
					c.Request().Header.Set("Authorization", "Bearer "+cookie.Value)
				}
			}
			return next(c)
		}
	})
	e.Use(echojwt.JWT([]byte(secret)))
	e.Use(utils.ExtractPlayerID)
	e.GET("/", JoinQueue)
	log.Println("Startig RPS Matchmaking Service")
	e.Logger.Fatal(e.Start(":" + os.Getenv("PORT")))
}
