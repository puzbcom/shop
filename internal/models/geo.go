package models

import "time"

type Country struct {
	ID        uint   `gorm:"primaryKey"`
	Code      string `gorm:"size:2"`
	Name      string `gorm:"size:100"`
	ZoneID    uint
	Status    int `gorm:"default:1"`
	CreatedAt *time.Time
	UpdatedAt *time.Time
	DeletedAt *time.Time
}

func (Country) TableName() string { return "countries" }

type State struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	CountryID uint
	Status    int `gorm:"default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	Country *Country `gorm:"foreignKey:CountryID"`
}

func (State) TableName() string { return "states" }

type City struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	StateID   *uint
	CountryID *uint
	Cost      float64 `gorm:"type:decimal(20,2);default:0"`
	Status    int     `gorm:"default:1"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	State *State `gorm:"foreignKey:StateID"`
}

func (City) TableName() string { return "cities" }

type Area struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	CityID    uint
	Cost      float64 `gorm:"type:decimal(20,2);default:0"`
	Status    int     `gorm:"default:1"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	City *City `gorm:"foreignKey:CityID"`
}

func (Area) TableName() string { return "areas" }

type Zone struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255"`
	Status    int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Zone) TableName() string { return "zones" }

type Currency struct {
	ID           uint    `gorm:"primaryKey"`
	Name         string  `gorm:"size:255"`
	Symbol       string  `gorm:"size:255"`
	ExchangeRate float64 `gorm:"type:decimal(10,5);default:1"`
	Status       int     `gorm:"default:0"`
	Code         *string `gorm:"size:20"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Currency) TableName() string { return "currencies" }
