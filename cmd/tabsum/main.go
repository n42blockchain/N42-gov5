package main
import ("context";"crypto/sha256";"fmt";"os"
 "github.com/c2h5oh/datasize"
 "github.com/n42blockchain/N42/lib/kv"
 mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
 log "github.com/n42blockchain/N42/lib/log/v3"
 "github.com/n42blockchain/N42/modules")
func main(){
 modules.N42Init(); kv.ChaindataTablesCfg = modules.N42TableCfg
 db,err := mdbxkv.NewMDBX(log.New()).Path(os.Args[1]).Label(kv.ChainDB).MapSize(512*datasize.GB).Accede().Readonly().Open(context.Background())
 if err!=nil{panic(err)}
 tx,_ := db.BeginRo(context.Background()); defer tx.Rollback()
 for _,t := range os.Args[2:] {
  h := sha256.New(); n := 0
  c,err := tx.Cursor(t); if err!=nil{fmt.Printf("%-14s ERR %v\n",t,err);continue}
  for k,v,_ := c.First(); k!=nil; k,v,_=c.Next(){h.Write(k);h.Write(v);n++}
  c.Close()
  fmt.Printf("%-14s rows=%-9d sha=%x\n",t,n,h.Sum(nil)[:12])
 }
}
