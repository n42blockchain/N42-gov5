package main
import ("context";"fmt";"os";"time"
 "github.com/c2h5oh/datasize"
 "github.com/n42blockchain/N42/lib/kv"
 mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
 log "github.com/n42blockchain/N42/lib/log/v3"
 "github.com/n42blockchain/N42/modules"
 "github.com/n42blockchain/N42/modules/rawdb")
func main(){
 modules.N42Init(); kv.ChaindataTablesCfg = modules.N42TableCfg
 db,err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).MapSize(512*datasize.GB).Accede().Readonly().Open(context.Background())
 if err!=nil{panic(err)}
 tx,_ := db.BeginRo(context.Background()); defer tx.Rollback()
 var n uint64; fmt.Sscanf(os.Args[2],"%d",&n)
 h := rawdb.ReadHeaderByNumber(tx, n)
 if h==nil{fmt.Println("no header");return}
 fmt.Printf("block %d time=%d (%s)\n", n, h.Time, time.Unix(int64(h.Time),0).UTC())
}
