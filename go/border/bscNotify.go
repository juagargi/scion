// Handles the notifications for the Basic approach, i.e. just send one SCMP message to each source of traffic.

package main

import (
	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/l4"
	"github.com/scionproto/scion/go/lib/layers"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scmp"
)

const logEnabledBsc = true

func (r *Router) bscNotify() {
	for {
		np := <-*r.qosConfig.GetBasicNotification()
		// if r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetCongestionWarning().Approach == 0 {
		// if logEnabledBsc {
		// 	/*srcIA, _ := np.Qpkt.Rp.SrcIA()
		// 	srcHost, _ := np.Qpkt.Rp.SrcHost()
		// 	DstIA, _ := np.Qpkt.Rp.DstIA()
		// 	DstHost, _ := np.Qpkt.Rp.DstHost()
		// 	log.Debug("New notification packet", "SrcIA", srcIA, "SrcHost",
		// 	srcHost, "DstIA", DstIA, "DstHost", DstHost)*/
		// 	log.Debug("New notification packet", "NPkt", np, "Pkt ID", np.Qpkt.Rp.Id, "L4hdr", fmt.Sprintf("%s", np.Qpkt.Rp.GetL4Hdr()))
		// }
		go func(np *queues.NPkt) {
			bscCW := r.createBscCongWarn(np)
			// if logEnabledBsc {
			// 	log.Debug("Created basic congestion warning", "bscCW", bscCW, "Pkt ID", np.Qpkt.Rp.Id)
			// }
			r.sendBscNotificationSCMP(np.Qpkt, bscCW)
			// np.Qpkt.Rp.RefInc(-1)

		}(np)
		// }
	}

}

func (r *Router) sendBscNotificationSCMP(qp *queues.QPkt, info *scmp.InfoBscCW) {
	if logEnabledBsc {
		srcIA, _ := qp.Rp.SrcIA()
		srcHost, _ := qp.Rp.SrcHost()
		DstIA, _ := qp.Rp.DstIA()
		DstHost, _ := qp.Rp.DstHost()
		CurrIF, _ := qp.Rp.IFCurr()
		NextIF, _ := qp.Rp.IFNext()
		Consdir, _ := qp.Rp.ConsDirFlag()
		log.Debug("New queueing packet\n", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "\nQNo", qp.QueueNo, "Pkt ID",
			qp.Rp.Id, "Current IF", *CurrIF, "NextIF", *NextIF, "cons dir ", *Consdir, "l4hdrType", qp.Rp.L4Type)
	}
	notification, err, id := r.createBscSCMPNotification(qp, scmp.ClassType{Class: scmp.C_General, Type: scmp.T_G_BasicCongWarn}, info)
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

	if logEnabledBsc {
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
	}
	notification.Route()

}

func (r *Router) createBscSCMPNotification(qp *queues.QPkt,
	ct scmp.ClassType, info scmp.Info) (*rpkt.RtrPkt, error, string) {

	// if logEnabledBsc {
	// 	srcIA, _ := qp.Rp.SrcIA()
	// 	srcHost, _ := qp.Rp.SrcHost()
	// 	DstIA, _ := qp.Rp.DstIA()
	// 	DstHost, _ := qp.Rp.DstHost()
	// 	l4hdr := qp.Rp.GetL4Hdr()
	// 	log.Debug("New queueing packet", "SrcIA", srcIA, "SrcHost",
	// 		srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", qp.Rp.Id,
	// 		"\n L4Type", qp.Rp.L4Type, "L4Header", l4hdr)
	// }
	id := qp.Rp.Id
	sp, err := qp.Rp.CreateReplyScnPkt()
	if err != nil {
		log.Debug("Unable to CreateReplyScnPkt", "err", err)
		return nil, err, id
	}
	sp.HBHExt = nil
	sp.HBHExt = make([]common.Extension, 0, common.ExtnMaxHBH+1)
	/*MS: We classify the congestion warning as not erroneous and don't need the
	Basic congestion warning to be HBH*/
	ext := &layers.ExtnSCMP{Error: false, HopByHop: false}
	sp.HBHExt = append(sp.HBHExt, ext)

	//TODO (stygerma): Add SPSE with DRKey

	//TODO: receive the classtype as a parameter for this function according to the approach of the considered queue
	// ct = scmp.ClassType{Class: scmp.C_General, Type: scmp.T_G_BasicCongWarn}
	sp.Pld = scmp.PldFromQuotes(ct, info, qp.Rp.L4Type, qp.Rp.GetRaw)

	sp.L4 = scmp.NewHdr(ct, sp.Pld.Len())
	// log.Debug("Created SPkt reply", "sp", sp, "Pkt ID", id)
	reply, err := qp.Rp.CreateReply(sp) //HERE
	// if logEnabledBsc {
	// 	srcIA, _ := reply.SrcIA()
	// 	srcHost, _ := reply.SrcHost()
	// 	DstIA, _ := reply.DstIA()
	// 	DstHost, _ := reply.DstHost()
	// 	log.Debug("Created RPkt reply", "SrcIA", srcIA, "SrcHost",
	// 		srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", id)
	// }
	if err != nil {
		log.Debug("Unable to CreateReply", "err", err)
	}
	return reply, err, id
}

func (r *Router) createBscCongWarn(np *queues.NPkt) *scmp.InfoBscCW {
	//testing := r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetMinBandwidth()
	restriction := r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetCongestionWarning().InformationContent
	if restriction > 3 {
		log.Error("Unable to create congestion warning", "restriction on information content", restriction)
		return nil
	}
	bscCW := &scmp.InfoBscCW{}
	bscCW.ConsIngress = common.IFIDType(np.Qpkt.Rp.Ingress.IfID)

	// srcIA, err := np.Qpkt.Rp.SrcIA()
	// if err != nil {
	// 	log.Error("Unable to fetch Source IA of packet", "err", err)
	// 	return nil
	// }

	// if srcIA.Equal(np.Qpkt.Rp.Ctx.Conf.IA) {
	// 	bscCW.Path = &spath.Path{}
	// } else {
	// 	bscCW.Path = &spath.Path{
	// 		Raw:    np.Qpkt.Rp.Raw[(np.Qpkt.Rp).GetPathIdx():np.Qpkt.Rp.CmnHdr.HdrLenBytes()],
	// 		InfOff: np.Qpkt.Rp.CmnHdr.InfoFOffBytes() - (np.Qpkt.Rp).GetPathIdx(),
	// 		HopOff: np.Qpkt.Rp.CmnHdr.HopFOffBytes() - (np.Qpkt.Rp).GetPathIdx()}
	// }
	// if logEnabledBsc {
	// 	log.Debug("InfoBscCW", "ConsIngress", common.IFIDType(np.Qpkt.Rp.Ingress.IfID),
	// 		"QueueLength", (r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo]).GetLength(), "CurrBW",
	// 		r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetTokenBucket().CurrBW, "QueueFullness",
	// 		r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetFillLevel(), "Violation", np.Qpkt.Act.GetReason())
	// }
	if restriction > 0 {
		bscCW.QueueLength = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetLength())
	}
	if restriction > 1 {
		bscCW.CurrBW = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetTokenBucket().CurrBW)
		bscCW.QueueFullness = uint64(r.qosConfig.GetConfig().Queues[np.Qpkt.QueueNo].GetFillLevel())
	}
	if restriction > 2 {
		bscCW.Violation = uint64(np.Qpkt.Act.GetReason())
	}
	//bscCW := &scmp.InfoBscCW{}
	return bscCW
}
