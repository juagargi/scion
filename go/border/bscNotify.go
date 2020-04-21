// Handles the notifications for the Basic approach, i.e. just send one SCMP message to each source of traffic.

package main

import (
	"fmt"

	"github.com/scionproto/scion/go/border/qos/queues"
	"github.com/scionproto/scion/go/border/rpkt"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/l4"
	"github.com/scionproto/scion/go/lib/layers"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/scmp"
	"github.com/scionproto/scion/go/lib/spath"
)

const logEnabledBsc = true

func (r *Router) bscNotify() {
	for np := range r.qosConfig.GetNotification() {
		//if r.config.Queues[np.Qpkt.QueueNo].GetCongestionWarning().Approach == 0 {
		if logEnabledBsc {
			/*srcIA, _ := np.Qpkt.Rp.SrcIA()
			srcHost, _ := np.Qpkt.Rp.SrcHost()
			DstIA, _ := np.Qpkt.Rp.DstIA()
			DstHost, _ := np.Qpkt.Rp.DstHost()
			log.Debug("New notification packet", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost)*/
			log.Debug("New notification packet", "NPkt", np, "Pkt ID", np.Qpkt.Rp.Id, "L4hdr", fmt.Sprintf("%s", np.Qpkt.Rp.GetL4Hdr()))
		}
		bscCW := r.createBscCongWarn(np)
		if logEnabledBsc {
			log.Debug("Created basic congestion warning", "bscCW", bscCW, "Pkt ID", np.Qpkt.Rp.Id)
		}
		r.sendBscNotificationSCMP(np.Qpkt, bscCW)
		np.Qpkt.Rp.RefInc(-1)
		//}
	}

}

func (r *Router) sendBscNotificationSCMP(qp *queues.QPkt, info *scmp.InfoBscCW) {
	if logEnabledBsc {
		srcIA, _ := qp.Rp.SrcIA()
		srcHost, _ := qp.Rp.SrcHost()
		DstIA, _ := qp.Rp.DstIA()
		DstHost, _ := qp.Rp.DstHost()
		log.Debug("New queueing packet", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "QNo", qp.QueueNo, "Pkt ID",
			qp.Rp.Id)
	}
	notification, err, id := r.createBscSCMPNotification(qp, scmp.ClassType{Class: scmp.C_General, Type: scmp.T_G_BasicCongWarn}, info)
	if err != nil {
		log.Error("unable to create notification SCMP", "err", err)
		return
	}
	if logEnabledBsc {
		srcIA, _ := notification.SrcIA()
		srcHost, _ := notification.SrcHost()
		DstIA, _ := notification.DstIA()
		DstHost, _ := notification.DstHost()
		pub := qp.Rp.Ctx.Conf.BR.InternalAddr
		routerAddr := addr.HostFromIP(pub.IP)
		pld, _ := notification.Payload(false)
		l4hdr, _ := notification.L4Hdr(false)
		cwpld := pld.(*scmp.CWPayload)
		quotedl4, _ := l4.UDPFromRaw(cwpld.L4Hdr)
		log.Debug("New SCMP Notification", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "\n RtrAddr", routerAddr,
			"CurrBW", r.qosConfig.GetQueue(qp.QueueNo).GetTokenBucket().CurrBW,
			"Pkt ID", id,
			"\n L4", l4hdr,
			"\n Congestion Warning", pld, "\n L4Hdr", quotedl4) //,
	}
	notification.Route()

}

func (r *Router) createBscSCMPNotification(qp *queues.QPkt,
	ct scmp.ClassType, info scmp.Info) (*rpkt.RtrPkt, error, string) {

	if logEnabledBsc {
		srcIA, _ := qp.Rp.SrcIA()
		srcHost, _ := qp.Rp.SrcHost()
		DstIA, _ := qp.Rp.DstIA()
		DstHost, _ := qp.Rp.DstHost()
		l4hdr := qp.Rp.GetL4Hdr()
		log.Debug("New queueing packet", "SrcIA", srcIA, "SrcHost",
			srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", qp.Rp.Id,
			"\n L4Type", qp.Rp.L4Type, "L4Header", l4hdr)
	}
	id := qp.Rp.Id
	sp, err := qp.Rp.CreateReplyScnPkt()
	if err != nil {
		return nil, err, ""
	}
	sp.HBHExt = make([]common.Extension, 0, common.ExtnMaxHBH+1)
	/*MS: We classify the congestion warning as not erroneous and don't need the
	Basic congestion warning to be HBH*/
	ext := &layers.ExtnSCMP{Error: false, HopByHop: false}
	sp.HBHExt = append(sp.HBHExt, ext)

	//TODO (stygerma): Add SPSE with DRKey

	sp.Pld = scmp.NotifyPld(info, qp.Rp.L4Type, qp.Rp.GetRaw)

	sp.L4 = scmp.NewHdr(scmp.ClassType{Class: scmp.C_General, Type: scmp.T_G_BasicCongWarn}, sp.Pld.Len())
	log.Debug("Created SPkt reply", "sp", sp, "Pkt ID", id)
	reply, err := qp.Rp.CreateReply(sp)
	// if logEnabledBsc {
	// 	srcIA, _ := reply.SrcIA()
	// 	srcHost, _ := reply.SrcHost()
	// 	DstIA, _ := reply.DstIA()
	// 	DstHost, _ := reply.DstHost()
	// 	log.Debug("Created RPkt reply", "SrcIA", srcIA, "SrcHost",
	// 		srcHost, "DstIA", DstIA, "DstHost", DstHost, "Pkt ID", id)
	// }
	return reply, err, id
}

func (r *Router) createBscCongWarn(np *queues.NPkt) *scmp.InfoBscCW {
	restrictionPrint := r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetCongestionWarning()
	testing := r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetMinBandwidth()
	restriction := 3
	if logEnabledBsc {
		log.Debug("restrictions on information content", "restriction", restrictionPrint, "MinBW", testing)
	}
	if restriction > 3 {
		log.Error("Unable to create congestion warning", "restriction on information content", restriction)
		return nil
	}
	bscCW := &scmp.InfoBscCW{}
	bscCW.ConsIngress = common.IFIDType(np.Qpkt.Rp.Ingress.IfID)
	bscCW.Path = &spath.Path{
		Raw:    np.Qpkt.Rp.Raw[(np.Qpkt.Rp).GetPathIdx():np.Qpkt.Rp.CmnHdr.HdrLenBytes()],
		InfOff: np.Qpkt.Rp.CmnHdr.InfoFOffBytes() - (np.Qpkt.Rp).GetPathIdx(),
		HopOff: np.Qpkt.Rp.CmnHdr.HopFOffBytes() - (np.Qpkt.Rp).GetPathIdx()}
	if logEnabledBsc {
		log.Debug("InfoBscCW", "ConsIngress", common.IFIDType(np.Qpkt.Rp.Ingress.IfID),
			"QueueLength", (r.qosConfig.GetQueue(np.Qpkt.QueueNo)).GetLength(), "CurrBW",
			r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetTokenBucket().CurrBW, "QueueFullness",
			r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetFillLevel(), "Violation", np.Qpkt.Act.GetReason())
	}
	if restriction > 0 {
		bscCW.QueueLength = uint64(r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetLength())
	}
	if restriction > 1 {
		bscCW.CurrBW = uint64(r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetTokenBucket().CurrBW)
		bscCW.QueueFullness = uint64(r.qosConfig.GetQueue(np.Qpkt.QueueNo).GetFillLevel())
	}
	if restriction > 2 {
		bscCW.Violation = uint64(np.Qpkt.Act.GetReason())
		//bscCW.ClassRule = np.Qpkt.Act.Rule
	}
	//bscCW := &scmp.InfoBscCW{}
	return bscCW
}
