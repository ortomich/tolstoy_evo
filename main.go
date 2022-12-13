package main

import (
	"database/sql"
	"fmt"
	"os"
	"log"
	"net/http"
	"encoding/json"
	"math/big"
	"time"
	"context"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)


func reconnect(rpc_index *int) (bind.ContractBackend) {
	for {
		rpc_env := fmt.Sprintf("RPC_URL_%v", *rpc_index)
		rpc_url := os.Getenv(rpc_env)
		if rpc_url == "" {
			*rpc_index = 1
			continue
		}
		log.Printf("Trying to connect to %v", rpc_url)
		сlient, err := ethclient.Dial(rpc_url)
		if err != nil {
			log.Printf("Failed to connect to blockhain:", err)
			time.Sleep(time.Duration(30)*time.Second)
			*rpc_index = *rpc_index + 1
			continue
		}
		log.Printf("Successfully connected to RPC %v", rpc_url)
		return сlient
	}
}


func runScanner(db *sql.DB) {
	log.Printf("Scanner started")
	rpc_index := 1
	client := reconnect(&rpc_index)

	unionWalletAddress := common.HexToAddress(os.Getenv("UNIONWALLET_CONTRACT"))
	unionWallet, err := NewUnionWallet(unionWalletAddress, client)
	if err != nil {
		log.Fatal("Failed to create Pool contract binding:", err)
	}

	unionWalletLinkSignature := []byte("Link(address,address)")
	unionWalletLinkSignatureHash := crypto.Keccak256Hash(unionWalletLinkSignature)
    log.Printf("unionWalletLinkSignatureHash %v", unionWalletLinkSignatureHash.Hex())

	unionWalletUnLinkSignature := []byte("UnLink(address,address)")
	unionWalletUnLinkSignatureHash := crypto.Keccak256Hash(unionWalletUnLinkSignature)
    log.Printf("unionWalletUnLinkSignatureHash %v", unionWalletUnLinkSignatureHash.Hex())

	opts := new(bind.FilterOpts)
	opts.Start = 29367000
	opts.End = new(uint64)
	*opts.End = 29367050

	for {
		log.Printf("Querying blockchain for events in %v:%v range", opts.Start, *opts.End)

		query := ethereum.FilterQuery{
			FromBlock: big.NewInt(int64(opts.Start)),
			ToBlock:   big.NewInt(int64(*opts.End)),
			Addresses: []common.Address{
				unionWalletAddress
			},
			Topics: [][]common.Hash{
				{unionWalletLinkSignatureHash, unionWalletUnLinkSignatureHash},
			},
		}

		rawLogs, err := client.FilterLogs(context.Background(), query)
		if err != nil {
			log.Printf("Failed to query events: %v", err)
			log.Printf("Reconnecting")
			rpc_index = rpc_index + 1
			client = reconnect(&rpc_index)
			continue
		}

		log.Printf("Query=%v", query, "Logs=%v", rawLogs)

		for _, rawLog := range(rawLogs) {
			if rawLog.Address == unionWalletAddress && rawLog.Topics[0] == unionWalletLinkSignatureHash {
				unionWalletLog, err := unionWallet.ParseGameResEvent(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse unionWallet::Link %v", err)
				}
		
				log.Printf("Found unionWallet Link %v", rawLog)
				_, err = db.Exec("INSERT IGNORE INTO Link(tx, block_number, user, identity) VALUES (?, ?, ?, ?)",
					rawLog.TxHash.String(),
					rawLog.BlockNumber,
					unionWalletLog.user.Hex(),
					unionWalletLog.identity.Hex());
	
				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

			if rawLog.Address == unionWalletAddress && rawLog.Topics[0] == unionWalletUnLinkSignatureHash {
				unionWalletLog, err := unionWallet.ParseGameResEvent(rawLog)
				if err != nil {
					log.Fatalf("Unable to parse unionWallet::UnLink %v", err)
				}
		
				log.Printf("Found unionWallet Link %v", rawLog)
				_, err = db.Exec(("DELETE FROM Link WHERE user = %v", unionWalletLog.user.Hex()),
					rawLog.TxHash.String(),
					rawLog.BlockNumber,
					unionWalletLog.user.Hex(),
					unionWalletLog.identity.Hex());
	
				if err != nil {
					log.Fatal("Unable to insert to DB: %v", err)
				}
				continue
			}

		for {
			time.Sleep(time.Duration(5)*time.Second)
			header, err := client.HeaderByNumber(context.Background(), nil)
			if err != nil {
				log.Printf("Failed to get header:", err)
				rpc_index = rpc_index + 1
				client = reconnect(&rpc_index)
			}
			log.Printf("The most recent block is %v", header.Number)
			if header.Number.Uint64() < *opts.End + 50 + 20 {
				time.Sleep(time.Duration(120)*time.Second)
			} else {
				break
			}
		}
		opts.Start = *opts.End
		*opts.End = *opts.End + 50
	}

	log.Printf("No new events")
}

func ensureDatabases(db *sql.DB) {
	log.Printf("Creating table Link")
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS Link(
		tx CHAR(100) PRIMARY KEY,
		block_number BIGINT NOT NULL,
		user CHAR(42) NOT NUL,
		identity CHAR(42) NOT NUL
	)`)
	if err != nil {
		log.Fatal("Failed to create table:", err)
	}
}


func main() {
	passwd := os.Getenv("MYSQL_ROOT_PASSWORD")
	dsn := fmt.Sprintf("root:%v@tcp(db:3306)/pnl", passwd)
	log.Printf("Connecting to %v", dsn)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal("Unable to connect to mysql", err)
	}
	db.SetConnMaxLifetime(0)
	db.SetMaxIdleConns(50)
	db.SetMaxOpenConns(50)

	ensureDatabases(db)

	s := &Service{db: db}
	go runScanner(db)
	http.ListenAndServe(":8080", s)
}

type Service struct {
	db *sql.DB
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	db := s.db
	switch r.URL.Path {
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return

	case "/allLinks":
		type Link = struct {
			User string `json:"user"`
			Identity string `json:"identity"`
		}
		type Response = struct {
			Data []Link `json:"data"`
		};
		var resp Response

		rows, err := db.Query("SELECT user, identity FROM Link;")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for rows.Next() {
			var entry Link
			err = rows.Scan(&entry.User, &entry.Identity)
			if err != nil {
				break
			}
			resp.Data = append(resp.Data, entry)
		}
		// Check for errors during rows "Close".
		// This may be more important if multiple statements are executed
		// in a single batch and rows were written as well as read.
		if closeErr := rows.Close(); closeErr != nil {
			http.Error(w, closeErr.Error(), http.StatusInternalServerError)
			return
		}

		// Check for row scan error.
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Check for errors during row iteration.
		if err = rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		json.NewEncoder(w).Encode(resp)
		return

	}
}