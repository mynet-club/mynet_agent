package mynet

import (
	"encoding/binary"
	"time"
)

// visitDNSRecords 遍历答案/权威/附加区；OPT 的 TTL 是扩展标志，不能老化。
func visitDNSRecords(msg []byte, visit func(typ uint16, ttlOffset int)) {
	if len(msg) < 12 {
		return
	}
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(msg[4:6])); i++ {
		_, next, err := parseDNSQName(msg, off)
		if err != nil || next+4 > len(msg) {
			return
		}
		off = next + 4
	}
	count := int(binary.BigEndian.Uint16(msg[6:8])) + int(binary.BigEndian.Uint16(msg[8:10])) + int(binary.BigEndian.Uint16(msg[10:12]))
	for i := 0; i < count; i++ {
		_, next, err := parseDNSQName(msg, off)
		if err != nil || next+10 > len(msg) {
			return
		}
		size := int(binary.BigEndian.Uint16(msg[next+8 : next+10]))
		if next+10+size > len(msg) {
			return
		}
		typ := binary.BigEndian.Uint16(msg[next : next+2])
		if typ != 41 {
			visit(typ, next+4)
		}
		off = next + 10 + size
	}
}

func limitDNSResponseTTL(msg []byte, capTTL uint32) time.Duration {
	min := capTTL
	found := false
	visitDNSRecords(msg, func(_ uint16, off int) {
		ttl := binary.BigEndian.Uint32(msg[off : off+4])
		if ttl > capTTL {
			ttl = capTTL
			binary.BigEndian.PutUint32(msg[off:off+4], ttl)
		}
		if !found || ttl < min {
			min = ttl
		}
		found = true
	})
	if !found {
		return 0
	}
	// 同一应答的地址和 CNAME 统一缩短到最小 TTL；OS 不能比路由租约保留更久。
	visitDNSRecords(msg, func(_ uint16, off int) { binary.BigEndian.PutUint32(msg[off:off+4], min) })
	return time.Duration(min) * time.Second
}

func ageDNSResponse(msg []byte, age uint32) {
	visitDNSRecords(msg, func(_ uint16, off int) {
		ttl := binary.BigEndian.Uint32(msg[off : off+4])
		if ttl > age {
			ttl -= age
		} else {
			ttl = 0
		}
		binary.BigEndian.PutUint32(msg[off:off+4], ttl)
	})
}
