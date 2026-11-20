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


def revoke():
    run=read("run.json")
    if run:
        run["revoked"]=True
        save("run.json",run)


def exchange(address, request):
    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as conn:
        conn.settimeout(15)
        conn.connect(str(address))
        conn.sendall(json.dumps(request).encode()+b"\n")
        with conn.makefile("rb") as stream:
            line=stream.readline(65537)
        require(line.endswith(b"\n") and len(line)<=65536,"服务响应无效")
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
        absent=[]
        selected={}
        for field in fields:
            options=reg.get("resources",{}).get(field,{})
            alias=selection.get(field) or next(iter(options),None)
            if not options.get(alias):
                absent.append(field)
            else:
                selected[field]=options[alias]
        if not spec.get("socket"):
            absent.append("受控服务绑定")
        elif not absent:
            try:
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
    require(not previous or previous["expires"]<=now or (previous["revoked"] and not previous.get("pending")),"已有真实写运行")
    require(isinstance(req["issue"],str) and req["issue"].strip(),"缺少 issue")
    require(type(req["count"]) is int and req["count"]>0 and type(req["ttl"]) is int and req["ttl"]>0,"次数和有效期必须为正整数")
    selected=req["resources"]
    require(isinstance(selected,dict) and {"identity","session","project"}<=selected.keys(),"缺少身份、会话或项目")
    resolved={k:reg["resources"][k][alias] for k,alias in selected.items()}
    require(all(isinstance(x,str) and x for x in resolved.values()),"资源未绑定")
    operations={}
    for op in req["operations"]:
        scene=reg["operations"][op]
        require(set(SCENES[scene])<=resolved.keys(),"操作缺少测试资源")
        spec=reg["capabilities"][scene]
        require(exchange(spec["socket"],{"version":1,"method":"readiness","scene":scene,"resources":resolved}).get("ready") is True,"服务能力未就绪")
        operations[op]=spec["socket"]
    require(operations,"缺少允许操作")
    token=secrets.token_urlsafe(32)
    run={"id":secrets.token_hex(16),"issue":req["issue"],"candidate":lock,"aliases":selected,"resources":resolved,
         "operations":operations,"remaining":req["count"],"expires":int(time.time())+req["ttl"],
         "tokenHash":hashlib.sha256(token.encode()).hexdigest(),"revoked":False}
    save("run.json",run)
    event("创建运行",run=run["id"],issue=run["issue"],candidate=lock,resources=selected,operations=list(operations),count=req["count"],expires=run["expires"])
    return {"run":run["id"],"credential":token,"expires":run["expires"]}


def dispatch(req):
    run=read("run.json")
    require(run and req["version"]==1 and run["id"]==req["run"] and
            hmac.compare_digest(run["tokenHash"],hashlib.sha256(req["credential"].encode()).hexdigest()),"运行凭据无效")
    op=req["operation"]
    require(not run["revoked"] and time.time()<run["expires"] and run["remaining"]>0 and op in run["operations"],"运行授权已失效")
    lock=read("candidate.json")
    require(lock==run["candidate"],"候选锁已更新")
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
    run.pop("pending")
    run["revoked"]=False
    save("run.json",run)
    return reply


def serve():
    path=PRIVATE/"service.sock"
    with (PRIVATE/"service.lock").open("a") as guard:
        fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        path.unlink(missing_ok=True)
        with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as server:
            server.bind(str(path.relative_to(ROOT)))
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
                        conn.sendall(json.dumps(result).encode()+b"\n")
                    except OSError:
                        pass


def main():
    parser=argparse.ArgumentParser(description="候选验收管理")
    parser.add_argument("action",choices=["init","register","start","stop","rollback","lock","preflight","open","revoke","service","samples","evidence","demo"])
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
