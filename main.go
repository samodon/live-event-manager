package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Category int

const (
	Clothing Category = iota
	Electronics
	Books
	Toys
)

type Event struct {
	gorm.Model
	Title     string
	StartDate time.Time
	Duration  time.Duration

	ImageURL     string `gorm:"type:varchar(255);"`
	InfluencerID uint
	Influencer   User      `gorm:"foreignKey:InfluencerID"`
	Products     []Product `gorm:"many2many:event_products;"`
	Purchases    []Purchase
}

type User struct {
	gorm.Model
	Email         string `gorm:"type:varchar(255);uniqueIndex"`
	Name          string `gorm:"type:varchar(255)"`
	IsInfluencer  bool
	CreatedEvents []Event `gorm:"foreignKey:InfluencerID"`
	Purchases     []Purchase
}

type Purchase struct {
	gorm.Model
	UserID       uint
	User         User `gorm:"foreignKey:UserID"`
	EventID      uint
	Event        Event `gorm:"foreignKey:EventID"`
	ProductID    uint
	Product      Product `gorm:"foreignKey:ProductID"`
	Quantity     int
	Price        float64
	PurchaseDate time.Time
}

type Product struct {
	gorm.Model
	Name     string
	Price    float64
	Category string
	ImageURL string  `gorm:"type:varchar(255);"`
	Events   []Event `gorm:"many2many:event_products;"`
}
type EventProduct struct {
	gorm.Model
	EventID   uint
	ProductID uint
	Quantity  int
}

type Main_User struct {
	user     User
	events   map[string][]Event
	products []Product
}

func parseTime(value string) time.Time {
	t, err := time.Parse("01/02/2006", value)
	if err != nil {
		log.Fatal(err)
	}
	return t
}

type EventData struct {
	Events []Event
}

func main() {
	db, err := initDB()
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	fsStatic := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fsStatic))

	// Serve uploaded files from the "uploads" directory
	fsUploads := http.FileServer(http.Dir("uploads"))
	http.Handle("/uploads/", http.StripPrefix("/uploads/", fsUploads))

	// Routes
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		tmpl := template.Must(template.ParseFiles("index.html"))

		var events []Event
		db.Find(&events)

		var products []Product
		db.Find(&products)

		// Filter events into live and upcoming
		now := time.Now()
		var liveEvents, upcomingEvents []Event
		for _, event := range events {
			endDate := event.StartDate.Add(event.Duration)
			if endDate.Before(now) {
				liveEvents = append(liveEvents, event)
			} else {
				upcomingEvents = append(upcomingEvents, event)
			}
		}

		data := struct {
			LiveEvents     []Event
			UpcomingEvents []Event
			Products       []Product
		}{
			LiveEvents:     liveEvents,
			UpcomingEvents: upcomingEvents,
			Products:       products,
		}

		err := tmpl.Execute(w, data)
		if err != nil {
			http.Error(w, "Error rendering template", http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/add-event", addEventHandler)
	http.HandleFunc("/event/", eventHandler)
	http.HandleFunc("/add-product", addProductHandler)
	http.HandleFunc("/dashboard-data", getDashboardData)
	// go simulatePurchases()
	http.HandleFunc("/recent-purchases", getRecentPurchases)
	log.Fatal(http.ListenAndServe(":8000", nil))
}

func eventHandler(w http.ResponseWriter, r *http.Request) {
	db, err := initDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	eventID, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	var event Event
	if err := db.Preload("Products").First(&event, eventID).Error; err != nil {
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	// Calculate remaining time
	remainingTime := calculateRemainingTime(event)

	// Create a new struct to hold the event and remaining time
	data := struct {
		Event         Event
		RemainingTime string
	}{
		Event:         event,
		RemainingTime: remainingTime,
	}

	tmpl := template.Must(template.ParseFiles("event_page.html"))
	err = tmpl.Execute(w, data)
	if err != nil {
		log.Println(err) // Print the error to the console
		http.Error(w, "Error rendering template", http.StatusInternalServerError)
	}
}

func addEventHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		title := r.FormValue("title")
		startDateStr := r.FormValue("start_date")
		durationStr := r.FormValue("duration")

		startDate := parseTime(startDateStr)
		duration, err := time.ParseDuration(durationStr + "h")
		if err != nil {
			http.Error(w, "Invalid duration format", http.StatusBadRequest)
			return
		}

		file, fileHeader, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "Error retrieving file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		dir := "./uploads"
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			http.Error(w, "Error creating upload directory", http.StatusInternalServerError)
			return
		}

		filePath := filepath.Join(dir, fileHeader.Filename)

		out, err := os.Create(filePath)
		if err != nil {
			http.Error(w, "Error saving file", http.StatusInternalServerError)
			return
		}
		defer out.Close()

		if _, err = io.Copy(out, file); err != nil {
			http.Error(w, "Error saving file", http.StatusInternalServerError)
			return
		}

		db, err := initDB()
		if err != nil {
			http.Error(w, "Database connection error", http.StatusInternalServerError)
			return
		}

		event := Event{
			Title:        title,
			StartDate:    startDate,
			Duration:     duration,
			ImageURL:     filePath,
			InfluencerID: 1, // Set a default or handle this accordingly
		}

		if result := db.Create(&event); result.Error != nil {
			http.Error(w, "Error creating event", http.StatusInternalServerError)
			return
		}

		productIDs := r.Form["products"]
		var products []Product
		if len(productIDs) > 0 {
			if err := db.Where("id IN ?", productIDs).Find(&products).Error; err != nil {
				http.Error(w, "Error retrieving products", http.StatusInternalServerError)
				return
			}
			db.Model(&event).Association("Products").Append(&products)
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func initDB() (*gorm.DB, error) {
	dsn := "jovane_samuels:samodon@tcp(127.0.0.1:3306)/live_shopping_events?charset=utf8mb4&parseTime=True&loc=Local"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	db.AutoMigrate(&User{}, &Event{}, &Product{}, &Purchase{}, &EventProduct{})
	insertTestData(db)
	return db, nil
}

func insertTestData(db *gorm.DB) {
	var user1, user2, user3 User
	db.FirstOrCreate(&user1, User{Email: "alice@example.com"}, User{Name: "Alice", IsInfluencer: true})
	db.FirstOrCreate(&user2, User{Email: "bob@example.com"}, User{Name: "Bob", IsInfluencer: false})
	db.FirstOrCreate(&user3, User{Email: "charlie@example.com"}, User{Name: "Charlie", IsInfluencer: true})

	var product1, product2, product3, product4 Product
	db.FirstOrCreate(&product1, Product{Name: "Cool T-shirt", ImageURL: "/upload/product1.jpg"}, Product{Price: 19.99, Category: "Clothing"})
	db.FirstOrCreate(&product2, Product{Name: "Smartphone", ImageURL: "/upload/product2.jpg"}, Product{Price: 299.99, Category: "Electronics"})
	db.FirstOrCreate(&product3, Product{Name: "Designer Handbag", ImageURL: "/upload/product3.jpg"}, Product{Price: 99.99, Category: "Accessories"})
	db.FirstOrCreate(&product4, Product{Name: "Gaming Console", ImageURL: "/upload/product4.jpg"}, Product{Price: 499.99, Category: "Electronics"})

	var event1, event2, event3 Event
	db.FirstOrCreate(&event1, Event{
		Title:        "Summer Sale",
		StartDate:    parseTime("08/28/2024"),
		Duration:     time.Hour * 24,
		InfluencerID: user1.ID,
		ImageURL:     "/upload/event1.jpg",
	})
	db.FirstOrCreate(&event2, Event{
		Title:        "Back to School",
		StartDate:    parseTime("09/01/2024"),
		Duration:     time.Hour * 48,
		InfluencerID: user3.ID,
		ImageURL:     "/upload/event2.jpg",
	})
	db.FirstOrCreate(&event3, Event{
		Title:        "Black Friday",
		StartDate:    parseTime("11/27/2024"),
		Duration:     time.Hour * 72,
		InfluencerID: user1.ID,
		ImageURL:     "/upload/event3.jpg",
	})

	if event1.ID == 0 {
		event1.Products = []Product{product1, product2}
		db.Save(&event1)
	}
	if event2.ID == 0 {
		event2.Products = []Product{product3, product4}
		db.Save(&event2)
	}
	if event3.ID == 0 {
		event3.Products = []Product{product1, product2, product3, product4}
		db.Save(&event3)
	}

	// Create Purchases
	// var purchase1, purchase2, purchase3 Purchase
	// db.FirstOrCreate(&purchase1, Purchase{
	// 	UserID:    user2.ID,
	// 	EventID:   event1.ID,
	// 	ProductID: product1.ID,
	// }, Purchase{
	// 	Quantity:     2,
	// 	PurchaseDate: time.Now(),
	// })
	// db.FirstOrCreate(&purchase2, Purchase{
	// 	UserID:    user2.ID,
	// 	EventID:   event2.ID,
	// 	ProductID: product3.ID,
	// }, Purchase{
	// 	Quantity:     1,
	// 	PurchaseDate: time.Now(),
	// })
	// db.FirstOrCreate(&purchase3, Purchase{
	// 	UserID:    user3.ID,
	// 	EventID:   event3.ID,
	// 	ProductID: product2.ID,
	// }, Purchase{
	// 	Quantity:     3,
	// 	PurchaseDate: time.Now(),
	// })
	//
	var eventProduct1, eventProduct2, eventProduct3 EventProduct
	db.FirstOrCreate(&eventProduct1, EventProduct{EventID: event1.ID, ProductID: product1.ID}, EventProduct{Quantity: 100})
	db.FirstOrCreate(&eventProduct2, EventProduct{EventID: event2.ID, ProductID: product3.ID}, EventProduct{Quantity: 50})
	db.FirstOrCreate(&eventProduct3, EventProduct{EventID: event3.ID, ProductID: product2.ID}, EventProduct{Quantity: 200})
}

func addProductHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		name := r.FormValue("name")
		priceStr := r.FormValue("price")
		category := r.FormValue("category")

		price, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			http.Error(w, "Invalid price format", http.StatusBadRequest)
			return
		}

		file, handler, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "Error uploading image", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		imgDir := "uploads/"
		imgPath := imgDir + handler.Filename
		f, err := os.OpenFile(imgPath, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			http.Error(w, "Error saving image", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		io.Copy(f, file)

		imgURL := "/uploads/" + handler.Filename

		product := Product{
			Name:     name,
			Price:    price,
			Category: category,
			ImageURL: imgURL,
		}

		db, err := initDB()
		if err != nil {
			http.Error(w, "Database connection error", http.StatusInternalServerError)
			return
		}

		result := db.Create(&product)
		if result.Error != nil {
			http.Error(w, "Error creating product", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func calculateRemainingTime(event Event) string {
	now := time.Now()

	endTime := event.StartDate.Add(event.Duration)

	remainingTime := endTime.Sub(now)
	fmt.Println(remainingTime.Hours())
	hours := int(remainingTime.Hours()) * -1
	minutes := int(remainingTime.Minutes()) % 60 * -1
	seconds := int(remainingTime.Seconds()) % 60 * -1

	return fmt.Sprintf("%d hours, %d minutes, %d seconds", hours, minutes, seconds)
}

func getDashboardData(w http.ResponseWriter, r *http.Request) {
	db, err := initDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	var totalEarned float64
	var itemsSold int

	// Query the database to get the total earned and items sold
	var purchases []Purchase
	db.Find(&purchases)

	for _, purchase := range purchases {
		totalEarned += float64(purchase.Quantity) * purchase.Price
		itemsSold += purchase.Quantity
	}

	data := struct {
		TotalEarned float64 `json:"total_earned"`
		ItemsSold   int     `json:"items_sold"`
	}{
		TotalEarned: totalEarned,
		ItemsSold:   itemsSold,
	}

	json.NewEncoder(w).Encode(data)
}

var (
	ctx         = context.Background()
	redisClient *redis.Client
)

func initRedis() (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	_, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	return client, nil
}

func simulatePurchases() {
	db, err := initDB()
	if err != nil {
		log.Println(err)
		return
	}

	redisClient, err = initRedis()
	if err != nil {
		log.Println(err)
		return
	}

	ticker := time.NewTicker(time.Second * 10)
	defer ticker.Stop()

	for range ticker.C {
		var products []Product
		db.Find(&products)
		product := products[rand.Intn(len(products))]

		var users []User
		db.Find(&users)
		user := users[rand.Intn(len(users))]

		var events []Event
		db.Find(&events)
		event := events[rand.Intn(len(events))]

		// Create a new purchase
		purchase := Purchase{
			UserID:       user.ID,
			User:         user,
			EventID:      event.ID,
			Event:        event,
			ProductID:    product.ID,
			Product:      product,
			Quantity:     rand.Intn(5) + 1, // Random quantity between 1 and 5
			Price:        product.Price,
			PurchaseDate: time.Now(),
		}

		db.Create(&purchase)

		jsonPurchase, err := json.Marshal(purchase)
		if err != nil {
			log.Println(err)
			return
		}

		redisClient.RPush(ctx, "recent_purchases", jsonPurchase)

		log.Println("Simulated purchase:", purchase)
	}
}

func getRecentPurchases(w http.ResponseWriter, r *http.Request) {
	redisClient, err := initRedis()
	if err != nil {
		http.Error(w, "Redis error", http.StatusInternalServerError)
		return
	}

	recentPurchases, err := redisClient.LRange(ctx, "recent_purchases", 0, -1).Result()
	if err != nil {
		http.Error(w, "Redis error", http.StatusInternalServerError)
		return
	}

	var purchases []Purchase
	for _, purchase := range recentPurchases {
		var p Purchase
		err := json.Unmarshal([]byte(purchase), &p)
		if err != nil {
			http.Error(w, "JSON error", http.StatusInternalServerError)
			return
		}

		purchases = append(purchases, p)
	}

	json.NewEncoder(w).Encode(purchases)
}

func getLatestNotification(w http.ResponseWriter, r *http.Request) {
	redisClient, err := initRedis()
	if err != nil {
		http.Error(w, "Redis error", http.StatusInternalServerError)
		return
	}

	recentPurchases, err := redisClient.LRange(ctx, "recent_purchases", -1, -1).Result()
	if err != nil {
		http.Error(w, "Redis error", http.StatusInternalServerError)
		return
	}

	if len(recentPurchases) == 0 {
		http.Error(w, "No recent purchases", http.StatusNotFound)
		return
	}

	var purchase Purchase
	err = json.Unmarshal([]byte(recentPurchases[0]), &purchase)
	if err != nil {
		http.Error(w, "JSON error", http.StatusInternalServerError)
		return
	}

	notification := struct {
		Message string `json:"message"`
	}{
		Message: fmt.Sprintf("New purchase: %d x %s", purchase.Quantity, purchase.Product.Name),
	}

	json.NewEncoder(w).Encode(notification)
}
