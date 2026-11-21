"""候选验收管理入口。管理进程在目标工程目录内运行。"""
import argparse
import contextlib
import datetime
import fcntl
import hashlib
import hmac
import json
import os
import re
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from cluster import Cluster, PRIVATE, ROOT, command, require, save

SCENES = {"OAuth":("identity","session"), "目录":("identity",),
          "消息":("identity","chat","project"), "文件":("identity","project","file")}
SAMPLES = ROOT/"tests/fixtures/l1/samples.json"
EVIDENCE = ROOT/"tests/integration/feishu_staging_evidence.sql"


def read(name, default=None):
    path = PRIVATE/name
    return json.loads(path.read_text()) if path.exists() else default


@contextlib.contextmanager
def exclusive():
    with (PRIVATE/"manager.lock").open("a") as lock:
        fcntl.flock(lock,fcntl.LOCK_EX)
        yield


def event(action, **detail):
    with (PRIVATE/"events.jsonl").open("a") as out:
        print(json.dumps({"时间":datetime.datetime.now(datetime.timezone.utc).isoformat(),
                          "操作":action,**detail},ensure_ascii=False),file=out)


def verify(lock):
    require(lock and lock.get("active"),"尚未锁定运行中的候选")
    cluster=Cluster(read("target.json"))
    require(cluster.verify(lock["image"])==lock["pod"],"实例已更换，请重新核对并锁定")
    require(command("git","rev-parse","HEAD")==lock["sha"],"目标工程版本已改变")


WEB_OPERATIONS={"oauth.identity":"OAuth","directory.scope":"目录","directory.sample":"目录"}
MESSAGE_OPERATIONS={"message.receive","message.reply","message.receipt"}
MESSAGE_QUEUE={"message.claim","message.ack"}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,req,fp,code,msg,headers,newurl):
        return None


def receiver_call(operation,run=None,**fields):
    receiver=read("receiver.json")
    require(receiver,"接收端未登记")
    body={"Operation":operation,**fields}
    if run is not None:
        body.update(Run=run["id"],SHA=run["candidate"]["sha"],Pod=run["candidate"]["pod"])
    request=urllib.request.Request(receiver["origin"]+"/internal/candidate-messages",
        data=json.dumps(body).encode(),headers={"Content-Type":"application/json",
            "Authorization":"Bearer "+receiver["token"],"X-Omnirelay-Receiver-SHA":receiver["sha"],
            "X-Omnirelay-Receiver-Rollback":receiver["rollback"]})
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    with opener.open(request,timeout=32) as reply:
        raw=reply.read((2<<20)+1)
    require(len(raw)<=2<<20,"接收端响应过长")
    result=json.loads(raw)
    require(result.get("ReceiverSHA")==receiver["sha"],"接收端版本不匹配")
    require(result.get("ReceiverRollback")==receiver["rollback"],"接收端回退摘要不匹配")
    return result


def stop_messages(run,transfer,outbound):
    if not run or not run.get("messages"):
        return
    result=receiver_call("stop",run,StopTransfer=transfer,RevokeOutbound=outbound)
    if transfer:
        run["transferStopped"]=True
    if outbound:
        run["outboundRevoked"]=True
    if transfer and outbound:
        run.pop("openingMessages",None)
    save("run.json",run)
    event("暂停受控消息",run=run["id"],receiver_sha=result["ReceiverSHA"],transfer=transfer,outbound=outbound)


def validate_web(web):
    require(re.fullmatch(r"https://[a-z0-9]+(?:[.-][a-z0-9]+)+",web["origin"]), "需要独立 HTTPS 主机名")
    require(all(isinstance(web.get(k),str) and web[k] for k in (
        "appID","tenantKey","bootstrap","managerNode","ingressClass","ingressNamespace","tlsSecret")), "网页验收配置缺项")
    require(web.get("productionOrigin","").startswith("https://") and web["origin"]!=web["productionOrigin"].lower().rstrip("/"),"验收入口须独立于生产入口")
    require(isinstance(web.get("ingressLabels"),dict) and web["ingressLabels"], "需要固定入口控制器标签")


def revoke():
    (PRIVATE/"web"/"web.json").unlink(missing_ok=True)
    run=read("run.json")
    if run:
        run["revoked"]=True
        save("run.json",run)
        if run["expires"]>time.time():
            stop_messages(run,True,True)


def exchange(address, request):
    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as conn:
        conn.settimeout(45)
        conn.connect(str(address))
        conn.sendall(json.dumps(request).encode()+b"\n")
        with conn.makefile("rb") as stream:
            line=stream.readline(1048577)
        require(line.endswith(b"\n") and len(line)<=1048576,"服务响应无效")
        return json.loads(line)


def status(ready, owner, missing):
    return {"状态":"就绪" if ready else "缺项","负责人":owner,"缺项":missing,
            "恢复条件":"已满足" if ready else "补齐并复核："+"、".join(missing)}


def preflight(reg, lock, selection):
    owner=reg.get("owner") or "候选环境维护人"
    try:
        verify(lock)
        missing=[]
    except (ValueError,OSError,KeyError) as exc:
        missing=[str(exc)]
    report={"候选":status(not missing,owner,missing)}
    for scene,fields in SCENES.items():
        spec=reg.get("capabilities",{}).get(scene,{})
        managed_messages=scene=="消息" and (read("receiver.json") or not spec.get("socket"))
        if managed_messages:
            fields=("identity","project")
        absent=[]
        selected={}
        for field in fields:
            options=reg.get("resources",{}).get(field,{})
            alias=selection.get(field) or next(iter(options),None)
            if not options.get(alias):
                absent.append(field)
            else:
                selected[field]=options[alias]
        if managed_messages:
            resources=reg.get("resources",{})
            if not any(resources.get(k,{}).get(selection.get(k) or next(iter(resources.get(k,{})),None))
                       for k in ("chat","privateChat")):
                absent.append("专用群或私聊")
            try:
                receiver_call("status")
            except (ValueError,OSError,KeyError):
                absent.append("接收端版本与认证")
        elif not spec.get("socket"):
            absent.append("受控服务绑定")
        elif not absent:
            try:
                for field in ("callback","department","person","group"):
                    options=reg.get("resources",{}).get(field,{})
                    alias=selection.get(field) or next(iter(options),None)
                    if alias in options: selected[field]=options[alias]
                web=read("target.json",{}).get("web")
                if web and scene in ("OAuth","目录"):
                    selected.update(appID=web["appID"],tenantKey=web["tenantKey"])
                reply=exchange(spec["socket"],{"version":1,"method":"readiness","scene":scene,"resources":selected})
                require(reply.get("ready") is True,"服务能力未就绪")
            except (ValueError,OSError,KeyError):
                absent.append("受控服务只读探针")
        if missing:
            absent.append("候选就绪")
        report[scene]=status(not absent,spec.get("owner") or owner,absent)
    return report


def new_run(req, lock, reg):
    verify(lock)
    previous=read("run.json")
    now=time.time()
    require(not previous or previous["expires"]<=now or
            (previous["revoked"] and not previous.get("pending") and not previous.get("openingMessages")),"已有真实写运行")
    require(isinstance(req["issue"],str) and req["issue"].strip(),"缺少 issue")
    require(type(req["count"]) is int and req["count"]>0 and type(req["ttl"]) is int and req["ttl"]>0,"次数和有效期必须为正整数")
    selected=req["resources"]
    require(isinstance(selected,dict) and {"identity","session","project"}<=selected.keys(),"缺少身份、会话或项目")
    resolved={k:reg["resources"][k][alias] for k,alias in selected.items()}
    require(all(isinstance(x,str) and x for x in resolved.values()),"资源未绑定")
    web=read("target.json",{}).get("web")
    requested=set(req["operations"])
    message_ops=requested.intersection(MESSAGE_OPERATIONS)
    publish_web=bool(requested.intersection(WEB_OPERATIONS) or message_ops)
    require(not publish_web or web,"缺少候选网页固定入口")
    if publish_web:
        resolved.update(appID=web["appID"],tenantKey=web["tenantKey"])
        require((not requested.intersection(WEB_OPERATIONS) or resolved.get("callback")==web["origin"]+"/auth/feishu/callback") and
                all((op in WEB_OPERATIONS and reg["operations"][op]==WEB_OPERATIONS[op]) or
                    (op in MESSAGE_OPERATIONS and reg["operations"][op]=="消息") for op in req["operations"]), "网页运行绑定不匹配")
    if message_ops:
        require("message.receive" in message_ops and (resolved.get("chat") or resolved.get("privateChat")) and
                req["ttl"]<=86400 and req["count"]<=1000,"消息运行缺少接收操作、专用会话或有效预算")
    expected_people=req.get("expectedPeople")
    require(expected_people is None or type(expected_people) is int and expected_people>=0 and "directory.sample" in req["operations"],"样本预期人数无效")
    operations={}
    for op in req["operations"]:
        if op in MESSAGE_OPERATIONS:
            continue
        scene=reg["operations"][op]
        require(set(SCENES[scene])<=resolved.keys(),"操作缺少测试资源")
        spec=reg["capabilities"][scene]
        require(exchange(spec["socket"],{"version":1,"method":"readiness","scene":scene,"resources":resolved}).get("ready") is True,"服务能力未就绪")
        operations[op]=spec["socket"]
    require(operations or message_ops,"缺少允许操作")
    token=secrets.token_urlsafe(32)
    run={"id":secrets.token_hex(16),"issue":req["issue"],"candidate":lock,"aliases":selected,"resources":resolved,
         "operations":operations,"remaining":req["count"],"expires":int(time.time())+req["ttl"],
         "tokenHash":hashlib.sha256(token.encode()).hexdigest(),"revoked":False}
    run["expectedPeople"]=expected_people
    if message_ops:
        receiver_call("status")
        run.update(messages=sorted(message_ops),openingMessages=True,revoked=True)
        save("run.json",run)
        try:
            result=receiver_call("open",run,Binding={"Tenant":resolved["tenantKey"],"App":resolved["appID"],
                "Sender":resolved["identity"],"Project":resolved["project"],"GroupChat":resolved.get("chat",""),
                "PrivateChat":resolved.get("privateChat",""),"FileToken":resolved.get("file",""),
                "Expires":datetime.datetime.fromtimestamp(run["expires"],datetime.timezone.utc).isoformat(),
                "Budget":req["count"],"MayReply":"message.reply" in message_ops,"MayReconcile":"message.receipt" in message_ops})
        except urllib.error.HTTPError as failure:
            if failure.code in (400,403,404):
                run.pop("openingMessages")
                save("run.json",run)
            raise
        run.pop("openingMessages")
        run["revoked"]=False
        event("接收端授权",run=run["id"],candidate_sha=lock["sha"],receiver_sha=result["ReceiverSHA"],
              receiver_rollback=result["ReceiverRollback"])
    save("run.json",run)
    event("创建运行",run=run["id"],issue=run["issue"],candidate=lock,resources=selected,operations=req["operations"],count=req["count"],expires=run["expires"])
    result={"run":run["id"],"credential":token,"expires":run["expires"]}
    if publish_web:
        try:
            save("web/web.json",{**result,"pod":lock["pod"],"messageProject":resolved["project"] if message_ops else ""})
            command("setfacl","-m","u:10000:r--",str(PRIVATE/"web/web.json"))
        except (ValueError,OSError):
            revoke()
            raise
    return result


def dispatch_message(req,run,lock):
    op=req["operation"]
    require(run.get("messages"),"运行未获准处理消息")
    cluster=Cluster(read("target.json"))
    require(cluster.candidate_pod(lock["image"])["metadata"]["uid"]==lock["pod"],"候选已换代")
    payload=req.get("payload") or {}
    fields={}
    if op in MESSAGE_QUEUE:
        require(not run.get("transferStopped"),"转交已关闭")
        if op=="message.claim":
            require(not payload,"领取参数不匹配")
        else:
            require(set(payload)=={"source","lease","revision"},"确认参数不匹配")
            fields={"Source":payload["source"],"Lease":payload["lease"],"Revision":int(payload["revision"])}
    else:
        require(op in run["messages"] and run["remaining"]>0 and not run.get("outboundRevoked"),"出站授权已失效")
        expected={"source","text"} if op=="message.reply" else {"source"}
        require(set(payload)==expected,"出站参数不匹配")
        fields={"Source":payload["source"],"Text":payload.get("text","")}
        require(isinstance(fields["Source"],str) and 0<len(fields["Source"])<=256 and len(fields["Text"])<=8192,"出站字段无效")
        run["remaining"]-=1
        save("run.json",run)
        event("受控出站",run=run["id"],operation=op)
    result=receiver_call(op,run,**fields)
    return {"finished":True,"component":"omnirelay@source:"+result["ReceiverSHA"],
            "message":result.get("Message"),"delivery":result.get("Delivery")}


def dispatch(req):
    run=read("run.json")
    require(run and req["version"]==1 and run["id"]==req["run"] and
            hmac.compare_digest(run["tokenHash"],hashlib.sha256(req["credential"].encode()).hexdigest()),"运行凭据无效")
    op=req["operation"]
    require(not run["revoked"] and time.time()<run["expires"],"运行授权已失效")
    lock=read("candidate.json")
    require(lock==run["candidate"],"候选锁已更新")
    if op in MESSAGE_QUEUE or op in {"message.reply","message.receipt"}:
        return dispatch_message(req,run,lock)
    require(run["remaining"]>0 and op in run["operations"],"运行授权已失效")
    verify(lock)
    require(time.time()<run["expires"],"运行已到期")
    run["remaining"]-=1
    # 先持久扣额；整个同步调用保持互斥。结果未知时冻结，等待人工核对。
    attempt=secrets.token_hex(16)
    run["revoked"]=True
    run["pending"]=attempt
    save("run.json",run)
    event("派发",run=run["id"],operation=op,attempt=attempt)
    reply=exchange(run["operations"][op],{"version":1,"method":"execute","operation":op,"run":run["id"],
                   "attempt":attempt,"expires":run["expires"],"resources":run["resources"],
                   "candidate":lock,"payload":req.get("payload",{})})
    require(reply.get("attempt")==attempt and reply.get("finished") is True,"服务结果待核对")
    if op in WEB_OPERATIONS:
        component=reply.get("component","")
        require(re.fullmatch(r"omnirelay@sha256:[a-f0-9]{64}",component),"缺少受控组件版本")
        sample=reply.get("sample") or {}
        people=list((sample.get("People") or {}).values())
        event("受控调用完成",run=run["id"],operation=op,component=component,
              succeeded=not bool(reply.get("error")),people=len(people),
              missingActivation=sum(p.get("status",{}).get("is_activated") is None for p in people),
              missingAvatar=sum(not p.get("avatar",{}).get("avatar_72") for p in people),
              departments=len(sample.get("Departments",{})),pages=sample.get("Pages",0),
              memberPages=sample.get("MemberPages",0),memberContinuations=sample.get("MemberContinuations",0),
              expectedPeople=run.get("expectedPeople"),multipage=sample.get("MultiPage",0))
    run.pop("pending")
    run["revoked"]=False
    save("run.json",run)
    return reply


def serve():
    web=read("target.json",{}).get("web")
    path=(PRIVATE/"web" if web else PRIVATE)/"service.sock"
    with (PRIVATE/"service.lock").open("a") as guard:
        fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        path.unlink(missing_ok=True)
        with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as server:
            server.bind(str(path.relative_to(ROOT)))
            if web:
                command("setfacl","-m","u:10000:rw-",str(path))
                manager_path=PRIVATE/"service.sock"
                manager_path.unlink(missing_ok=True)
                manager_path.symlink_to("web/service.sock")
            server.listen(8)
            while True:
                conn,_=server.accept()
                with conn:
                    try:
                        conn.settimeout(10)
                        with conn.makefile("rb") as stream:
                            line=stream.readline(65537)
                        require(len(line)<=65536 and line.endswith(b"\n"),"请求过长或不完整")
                        with exclusive():
                            result=dispatch(json.loads(line))
                    except (ValueError,OSError,KeyError,TypeError,AttributeError):
                        result={"error":"调用未获准或未确认完成"}
                    try:
                        conn.sendall(json.dumps(result,ensure_ascii=False).encode()+b"\n")
                    except OSError:
                        pass


def main():
    parser=argparse.ArgumentParser(description="候选验收管理")
    parser.add_argument("action",choices=["init","web","receiver","register","start","stop","rollback","lock","preflight","open","revoke",
        "hold-messages","revoke-outbound","service","samples","evidence","demo"])
    parser.add_argument("value",nargs="?")
    args=parser.parse_args()
    os.umask(0o077)
    os.chdir(ROOT)
    PRIVATE.mkdir(parents=True,exist_ok=True,mode=0o700)
    action=args.action
    if action=="service":
        serve()
        return
    with exclusive():
        cfg=read("target.json")
        lock=read("candidate.json")
        reg=read("registry.json",{})
        if action=="init":
            require(not cfg,"目标已登记")
            cfg=json.load(sys.stdin)
            for key in ("context","repository","ref","productionNamespace","productionLease"):
                require(isinstance(cfg[key],str) and cfg[key],"目标参数缺失")
            require(cfg["ref"].startswith("refs/heads/"),"需要完整仓库分支引用")
            for key in ("productionDatabase","productionObjects"):
                require(cfg[key]["service"] and type(cfg[key]["port"]) is int and 0<cfg[key]["port"]<65536,"生产对照端点无效")
            cfg["id"]=secrets.token_hex(12)
            save("target.json",cfg)
        elif action=="web":
            require(cfg and not (lock and lock["active"]), "请先登记目标并停止候选")
            web=json.load(sys.stdin)
            validate_web(web)
            require(cfg.get("delivery")!="node" or cfg["buildNode"]==web["managerNode"],"管理机与构建节点不一致")
            revoke()
            cfg["web"]=web
            save("target.json",cfg)
        elif action=="receiver":
            existing=read("run.json")
            require(not existing or not existing.get("messages") or existing["expires"]<=time.time() or
                    existing.get("transferStopped") and existing.get("outboundRevoked"),"请先关闭当前消息运行")
            data=json.load(sys.stdin)
            url=urllib.parse.urlsplit(data["origin"])
            require(url.scheme=="https" and url.hostname and not (url.username or url.password or url.path or url.query or url.fragment)
                    and re.fullmatch(r"[0-9a-f]{40}",data["sha"]) and re.fullmatch(r"[0-9a-f]{64}",data["token"])
                    and re.fullmatch(r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}",data["rollback"]),"接收端登记无效")
            save("receiver.json",data)
        elif action=="register":
            data=json.load(sys.stdin)
            require(isinstance(data["owner"],str) and data["owner"].strip(),"缺少负责人")
            for scene,spec in data.get("capabilities",{}).items():
                require(scene in SCENES and Path(spec["socket"]).is_absolute(),"服务绑定无效")
            revoke()
            save("registry.json",data)
        elif action in ("samples","demo"):
            samples=json.loads(SAMPLES.read_text())
            require(samples["场景样本"] and samples["账号"],"L1 匿名样本缺失")
            result={"L1 匿名样本":{"状态":"可执行","场景数":len(samples["场景样本"]),"负责人":"验收维护人","缺项":[],"恢复条件":"已满足"}}
            if action=="demo":
                result.update(preflight({},None,{}))
            save(action+".json",result)
            return result
        elif action=="preflight":
            result=preflight(reg,lock,json.loads(args.value or "{}"))
            save("preflight.json",result)
            return result
        elif action=="open":
            return new_run(json.load(sys.stdin),lock,reg)
        elif action=="revoke":
            revoke()
            event("撤销运行")
        elif action in ("hold-messages","revoke-outbound"):
            run=read("run.json")
            require(run and run.get("messages"),"没有消息运行")
            stop_messages(run,action=="hold-messages",action=="revoke-outbound")
        else:
            require(cfg,"尚未登记目标环境")
            cluster=Cluster(cfg)
            if action=="evidence":
                verify(lock)
                sql=EVIDENCE.read_text()
                resources=reg.get("evidence",{})
                env={"OMNIRELAY_EVIDENCE_"+k.upper():resources[k] for k in ("app_id","tenant_key","message_id","account_id")}
                require(all(isinstance(v,str) and v for v in env.values()),"证据查询绑定不完整")
                # 通过容器的标准输入传递绑定，使用非管理角色执行已有只读事务。
                bindings="".join("\\setenv "+k+" "+ "'"+v.replace("'", "''")+"'"+"\n" for k,v in env.items())
                require(all(re.fullmatch(r"[\w.-]+",v) for v in env.values()),"证据查询标识无效")
                output=command(*cluster.k,"exec","-i","deployment/db","--","psql","-X","-U","postgres","-d","candidate",
                               data="SET ROLE candidate_runtime;\n"+bindings+sql)
                save("evidence.json",{"结果":output})
                return
            if action=="lock":
                require(lock and lock["active"],"请先启动候选")
                uid=cluster.verify(lock["image"])
                require(command("git","rev-parse","HEAD")==lock["sha"],"源码版本不匹配")
                revoke()
                lock["pod"]=uid
                save("candidate.json",lock)
                return
            if action=="start":
                image=cluster.build(args.value or "")
                revision=args.value
            if action=="rollback":
                previous=read("previous.json")
                require(previous,"没有可回滚的候选")
                revision,image=previous["sha"],previous["image"]
                require(command("git","rev-parse","HEAD")==revision,"请先将目标工程同步至回滚 SHA")
            revoke()
            if lock:
                save("candidate.json",{**lock,"active":False})
            if action=="stop":
                cluster.owned()
                command(*cluster.k,"scale","deployment/app","--replicas=0")
                command(*cluster.k,"wait","--for=delete","pod","-l","role=server","--timeout=120s")
                event("候选停止",candidate=lock)
                return
            cluster.setup()
            cluster.launch(image)
            uid=cluster.verify(image)
            if lock and lock["image"]!=image:
                save("previous.json",lock)
            lock={"active":True,"sha":revision,"image":image,"pod":uid}
            save("candidate.json",lock)
            event("候选启动" if action=="start" else "候选回滚",candidate=lock)


if __name__=="__main__":
    try:
        result=main()
        if result is not None:
            print(json.dumps(result,ensure_ascii=False,indent=2))
    except (ValueError,OSError,KeyError,TypeError,subprocess.SubprocessError) as exc:
        sys.exit("候选操作失败："+str(exc))
