package bootstrapsuperadmin

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
	"user-service/internal/repository"
	
	"github.com/joho/godotenv"
)


func main() {
	if err := godotenv.Load("../../.env"); err != nil {
		log.Fatal(fmt.Errorf("load .env for super admin bootstrap: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	database_url := os.Getenv("DATABASE_URL")
	pool, err := repository.Open(ctx, database_url)
	if err != nil {
		log.Fatal(fmt.Errorf("Error connecting to DB when bootstrapping super admin. %w", err))
	}
	defer pool.Close()
	db := repository.NewPostgres(pool)



}

func indempotency_check() {

}
