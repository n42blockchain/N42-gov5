package main
import ("bytes";"context";"fmt";"os"
 "github.com/c2h5oh/datasize"
 "github.com/n42blockchain/N42/lib/kv"
 mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
 log "github.com/n42blockchain/N42/lib/log/v3"
 "github.com/n42blockchain/N42/modules")
func load(dir, table string) map[string][]byte {
 db,err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).MapSize(512*datasize.GB).Accede().Readonly().Open(context.Background())
 if err!=nil{panic(err)}
 defer db.Close()
 tx,_ := db.BeginRo(context.Background()); defer tx.Rollback()
 out := map[string][]byte{}
 c,_ := tx.Cursor(table)
 for k,v,_ := c.First(); k!=nil; k,v,_=c.Next(){ out[string(k)] = append([]byte(nil), v...) }
 c.Close()
 return out
}
func main(){
 modules.N42Init(); kv.ChaindataTablesCfg = modules.N42TableCfg
 a := load(os.Args[1], os.Args[3])
 b := load(os.Args[2], os.Args[3])
 n := 0
 for k,av := range a {
  bv,ok := b[k]
  if !ok { fmt.Printf("only-A %x\n", k); n++; continue }
  if !bytes.Equal(av,bv) { fmt.Printf("DIFF key=%x\n  A=%x\n  B=%x\n", k, av, bv); n++ }
 }
 for k := range b { if _,ok := a[k]; !ok { fmt.Printf("only-B %x\n", k); n++ } }
 fmt.Println("diffs:", n)
}
