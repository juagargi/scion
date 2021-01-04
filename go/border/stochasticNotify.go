//Implements the stochastic notification approach

package main

import (
	"math/rand"

	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/l4"
	"github.com/scionproto/scion/go/lib/layers"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scmp"
)

const (
	logEnabledStoch = false
)

func (r *Router) stochNotify() {
	for {
		np := <-*r.qosConfig.GetStochNotification()
		// log.Debug("New packet in notify method", "pkt id", np.Qpkt.Rp.Id)
		go func(np *queues.NPkt) {

			queueFullness := float64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetFillLevel())
			switchingPoint, output := (r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetPID()).NewControlUpdate(queueFullness)
			probs := r.calculateProbs(np.Qpkt, switchingPoint)
			random := rand.Intn(100)

			log.Debug("Stochastic Notify stats", "probs", probs, "random", random, "switching Point", switchingPoint, "PID output", output)
			if random <= probs {
				stochCW := r.createStochCongWarn(np)
				r.sendStochNotificationSCMP(np.Qpkt, stochCW)
				// np.Qpkt.Rp.RefInc(-1)

				// if uint8(np.Qpkt.Act.GetAction()) == 1 && np.Qpkt.Forward == true {
				// 	r.forwardPacket(np.Qpkt.Rp)
				// }

				// //Release packet if its action is DROPNOTIFY
				// if uint8(np.Qpkt.Act.GetAction()) == 3 {
				// 	np.Qpkt.Rp.Release()
				// }
				// np.Qpkt.Rp.Free(np.Qpkt.Rp)

			}
			var forwarded bool
			if uint8(np.Qpkt.Act.GetAction()) == 1 {
				np.Qpkt.Mtx.Lock()
				defer np.Qpkt.Mtx.Unlock()
				if np.Qpkt.Forward {
					log.Debug("Packet in Notify forwarded", "id", np.Qpkt.Rp.Id)
					r.forwardPacket(np.Qpkt.Rp)
					// qp.Mtx.Unlock()
					forwarded = true
				} else {
					np.Qpkt.Forward = true
					// qp.Mtx.Unlock()
					log.Debug("Packet in Notify forwarding enabled", "id", np.Qpkt.Rp.Id)
					forwarded = true
				}

			}

			// Release packet if it's action is DROPNOTIFY
			if !forwarded {
				if uint8(np.Qpkt.Act.GetAction()) == 3 {
					np.Qpkt.Rp.Release()
					log.Debug("Packet in Notify released", "id", np.Qpkt.Rp.Id)
				}
			}
		}(np)
		// np.Qpkt.Rp.RefInc(-1)

		// if uint8(np.Qpkt.Act.GetAction()) == 1 && np.Qpkt.Forward == true {
		// 	r.forwardPacket(np.Qpkt.Rp)
		// }

		// //Release packet if its action is DROPNOTIFY
		// if uint8(np.Qpkt.Act.GetAction()) == 3 {
		// 	np.Qpkt.Rp.Release()
		// }

		// }
	}
}

func (r *Router) sendStochNotificationSCMP(qp *queues.QPkt, info scmp.Info) {
	if logEnabledStoch {
		srcIA, _ := qp.Rp.SrcIA()
		srcHost, _ := qp.Rp.SrcHost()
		DstIA, _ := qp.Rp.DstIA()
		DstHost, _ := qp.Rp.DstHost()
		log.Debug("New queueing packet", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "QNo", qp.QueueNo, "Pkt ID",
			qp.Rp.Id)
	}
	notification, err, id := r.createStochSCMPNotification(qp, scmp.ClassType{Class: scmp.C_General, Type: scmp.T_G_StochasticCongWarn}, info)
	if err != nil {
		log.Error("unable to create notification SCMP", "err", err, "id", id)
		return
	}

	var forwarded bool
	if uint8(qp.Act.GetAction()) == 1 {
		qp.Mtx.Lock()
		if qp.Forward {
			log.Debug("Packet in Notify forwarded", "id", qp.Rp.Id)
			r.forwardPacket(qp.Rp)
			// qp.Mtx.Unlock()
			forwarded = true
		} else {
			qp.Forward = true
			qp.Mtx.Unlock()
			log.Debug("Packet in Notify forwarding enabled", "id", qp.Rp.Id)
			forwarded = true
		}

	}

	// Release packet if it's action is DROPNOTIFY
	if !forwarded {
		if uint8(qp.Act.GetAction()) == 3 {
			qp.Rp.Release()
			log.Debug("Packet in Notify released", "id", qp.Rp.Id)
		}
	}
	if logEnabledStoch {
		srcIA, _ := notification.SrcIA()
		srcHost, _ := notification.SrcHost()
		DstIA, _ := notification.DstIA()
		DstHost, _ := notification.DstHost()
		pld, _ := notification.Payload(false)
		l4, _ := notification.L4Hdr(false)
		log.Debug("New SCMP Notification", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", id,
			"\n L4", l4,
			"\n Congestion Warning", pld)
	}

	srcIA, _ := notification.SrcIA()
	srcHost, _ := notification.SrcHost()
	DstIA, _ := notification.DstIA()
	DstHost, _ := notification.DstHost()
	pld, _ := notification.Payload(false)
	l4hdr, _ := notification.L4Hdr(false)
	cwpld := pld.(*scmp.Payload)
	quotedl4, _ := l4.UDPFromRaw(cwpld.L4Hdr)
	// ifNext, _ := notification.IFNext()
	log.Debug("New SCMP Notification", "SrcIA", srcIA, "SrcHost",
		srcHost, "DstIA", DstIA, "DstHost", DstHost,
		"Pkt ID", id, "l4 hdr type", notification.L4Type,
		"\n L4", l4hdr,
		"\n Congestion Warning", pld, "\n L4Hdr", quotedl4, "HBH extension", notification.HBHExt, "id", id) //,r.qosConfig.GetQueue(qp.QueueNo).GetTokenBucket().CurrBW

	notification.Route()

}

func (r *Router) createStochSCMPNotification(qp *queues.QPkt,
	ct scmp.ClassType, info scmp.Info) (*rpkt.RtrPkt, error, string) {

	if logEnabledStoch {
		srcIA, _ := qp.Rp.SrcIA()
		srcHost, _ := qp.Rp.SrcHost()
		DstIA, _ := qp.Rp.DstIA()
		DstHost, _ := qp.Rp.DstHost()
		log.Debug("New queueing packet", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", qp.Rp.Id)
	}
	id := qp.Rp.Id
	sp, err := qp.Rp.CreateReplyScnPkt()
	if err != nil {
		return nil, err, ""
	}
	sp.HBHExt = nil
	sp.HBHExt = make([]common.Extension, 0, common.ExtnMaxHBH+1)
	/*MS: We classify the congestion warning as not erroneous and don't need the
	Basic congestion warning to be HBH*/
	ext := &layers.ExtnSCMP{Error: false, HopByHop: false}
	sp.HBHExt = append(sp.HBHExt, ext)
	//TODO (stygerma): Add SPSE with DRKey

	sp.Pld = scmp.PldFromQuotes(ct, info, qp.Rp.L4Type, qp.Rp.GetRaw)
	sp.L4 = scmp.NewHdr(ct, sp.Pld.Len())
	// log.Debug("Created SPkt reply", "sp", sp, "Pkt ID", id)
	reply, err := qp.Rp.CreateReply(sp)
	if logEnabledStoch {
		srcIA, _ := reply.SrcIA()
		srcHost, _ := reply.SrcHost()
		DstIA, _ := reply.DstIA()
		DstHost, _ := reply.DstHost()
		log.Debug("Created RPkt reply", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", id)
	}
	return reply, err, id
}

func (r *Router) createStochCongWarn(np *queues.NPkt) *scmp.InfoStochCW {
	restriction := r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetCongestionWarning().InformationContent

	if restriction > 3 {
		log.Error("Unable to create congestion warning", "restriction on information content", restriction)
		return nil
	}
	stochCW := &scmp.InfoStochCW{}
	stochCW.ConsIngress = common.IFIDType(np.Qpkt.Rp.Ingress.IfID)

	// srcIA, err := np.Qpkt.Rp.SrcIA()
	// if err != nil {
	// 	log.Error("Unable to fetch Source IA of packet", "err", err)
	// 	return nil
	// }

	// if srcIA.Equal(np.Qpkt.Rp.Ctx.Conf.IA) {
	// 	stochCW.Path = &spath.Path{}
	// } else {
	// 	stochCW.Path = &spath.Path{
	// 		Raw:    np.Qpkt.Rp.Raw[(np.Qpkt.Rp).GetPathIdx():np.Qpkt.Rp.CmnHdr.HdrLenBytes()],
	// 		InfOff: np.Qpkt.Rp.CmnHdr.InfoFOffBytes() - (np.Qpkt.Rp).GetPathIdx(),
	// 		HopOff: np.Qpkt.Rp.CmnHdr.HopFOffBytes() - (np.Qpkt.Rp).GetPathIdx()}
	// }
	if restriction > 0 {
		stochCW.QueueLength = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetLength())
	}
	if restriction > 1 {
		stochCW.CurrBW = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetTokenBucket().CurrBW)
		stochCW.QueueFullness = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetFillLevel())
	}
	if restriction > 2 {
		stochCW.Violation = uint64(np.Qpkt.Act.GetReason())
	}
	return stochCW
}

func (r *Router) calculateProbs(qp *queues.QPkt, switchingPoint int) int {
	queueFullness := (r.qosConfig.GetConfig().Queues[qp.QueueNo]).GetFillLevel()
	if queueFullness <= switchingPoint {
		return queueFullness
	}
	return (queueFullness - 1) / (queueFullness - switchingPoint)
}
