"""在目标机生成候选专用资源；实际清单和证书只在私有目录保存。"""
import json
import os
import re
import secrets
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
PRIVATE = ROOT / "deploy/k8s/private/candidate"
LABEL = "omnirelay.io/candidate"


def require(ok, message):
    if not ok:
        raise ValueError(message)


def command(*args, data=None):
    result = subprocess.run(args, input=data, capture_output=True, text=True)
    if result.returncode:
        raise ValueError(f"{Path(args[0]).name} 执行失败（退出码 {result.returncode}）")
    return result.stdout.strip()


def save(name, data):
    path = PRIVATE / name
    tmp = path.with_suffix(".tmp")
    with tmp.open("w") as out:
        json.dump(data,out,ensure_ascii=False,indent=2)
        out.flush()
        os.fsync(out.fileno())
    tmp.replace(path)
    directory=os.open(PRIVATE, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


class Cluster:
    def __init__(self, cfg):
        self.cfg = cfg
        self.ns = "candidate-" + cfg["id"]
        require(re.fullmatch("[a-f0-9]{24}", cfg["id"]), "候选标识无效")
        self.k = ["kubectl", "--context", cfg["context"], "-n", self.ns]

    def get(self, *args):
        return json.loads(command(*self.k, "get", *args, "-o", "json"))

    def apply(self, obj):
        command(*self.k, "apply", "-f", "-", data=json.dumps(obj))

    def owned(self):
        ns = self.get("namespace", self.ns)
        require(ns["metadata"]["labels"].get(LABEL) == self.cfg["id"], "候选命名空间归属不匹配")

    def build(self, sha):
        cfg = self.cfg
        require(re.fullmatch("[0-9a-f]{40}", sha), "需要完整源码 SHA")
        remote = command("git", "ls-remote", "--exit-code", "origin", cfg["ref"]).split()
        require(remote == [sha, cfg["ref"]], "仓库中的源码版本不匹配")
        require(command("git", "rev-parse", "HEAD") == sha and not command("git", "status", "--porcelain", "--untracked-files=normal"), "目标工程未同步或含未提交内容")
        tag = cfg["repository"] + ":" + sha
        # 从 Git 对象构建，防止忽略目录中的额外文件混入源码。
        archive = subprocess.Popen(["git", "archive", sha], stdout=subprocess.PIPE)
        try:
            args=["--build-arg", "GO_MODULE_PROXY"] if os.getenv("GO_MODULE_PROXY") else []
            if os.getenv("CANDIDATE_PIP_INDEX"):
                args.extend(["--build-arg","CANDIDATE_PIP_INDEX"])
            subprocess.run(["docker", "build", *args, "--label", "org.opencontainers.image.revision="+sha,
                            "-f", "deploy/k8s/base/go/image.Dockerfile", "-t", tag, "-"],
                           stdin=archive.stdout, check=True)
        finally:
            archive.stdout.close()
            require(archive.wait() == 0, "源码归档失败")
        if cfg.get("delivery")=="node":
            digest=command("docker","image","inspect","--format","{{.Id}}",tag)
            require(re.fullmatch(r"sha256:[a-f0-9]{64}",digest),"构建镜像缺少内容摘要")
            command("docker","tag",tag,cfg["repository"]+":"+digest.replace(":","-"))
            return digest
        command("docker", "push", tag)
        refs = json.loads(command("docker", "image", "inspect", "--format", "{{json .RepoDigests}}", tag))
        images = [x for x in refs if x.startswith(cfg["repository"]+"@sha256:")]
        require(len(images) == 1, "构建镜像摘要不唯一")
        return images[0]

    def object(self, kind, name, **body):
        api = {"Deployment":"apps/v1", "Job":"batch/v1", "NetworkPolicy":"networking.k8s.io/v1","Ingress":"networking.k8s.io/v1"}.get(kind,"v1")
        return {"apiVersion":api,"kind":kind,"metadata":{"name":name,"namespace":self.ns}, **body}

    def setup(self):
        ident = self.cfg["id"]
        existing = command(*self.k,"get","namespace",self.ns,"--ignore-not-found","-o","name")
        if not existing:
            ns = {"apiVersion":"v1","kind":"Namespace","metadata":{"name":self.ns,"labels":{LABEL:ident}}}
            command(*self.k,"create","-f","-",data=json.dumps(ns))
        self.owned()
        cert = PRIVATE / "db.crt"
        key = PRIVATE / "db.key"
        if not cert.exists():
            require(not key.exists(), "数据库证书生成未完成")
            command("openssl","req","-x509","-newkey","rsa:3072","-nodes","-days","365",
                    "-subj","/CN=db","-addext","subjectAltName=DNS:db","-keyout",str(key),"-out",str(cert))
        passwords = PRIVATE / "db.json"
        if not passwords.exists():
            save("db.json",{k:secrets.token_hex(32) for k in ["POSTGRES_PASSWORD","MIGRATION_PASSWORD","RUNTIME_PASSWORD"]})
        pw = json.loads(passwords.read_text())
        resources = [self.object("ServiceAccount","candidate",automountServiceAccountToken=False)]
        def secret(name, data):
            resources.append(self.object("Secret",name,stringData=data))
        secret("pg-admin",{**pw,"POSTGRES_DB":"candidate","CANDIDATE_ID":ident,"PGDATA":"/data/pg"})
        secret("pg-tls",{"db.crt":cert.read_text(),"db.key":key.read_text()})
        resources.append(self.object("ConfigMap","pg-ca",data={"db.crt":cert.read_text()}))
        resources.append(self.object("ConfigMap","pg-init",data={"init.sql":Path(__file__).with_name("init.sql").read_text()}))
        for role in ("migration","runtime"):
            secret(role,{"OMNIRELAY_DATABASE_URL":f"postgresql://candidate_{role}:{pw[role.upper()+'_PASSWORD']}@db/candidate?sslmode=verify-full&sslrootcert=/ca/db.crt",
                         "OMNIRELAY_CANDIDATE":"1","OMNIRELAY_CANDIDATE_ID":ident})
        for name, port in [("app",8080),("db",5432)]:
            resources.append(self.object("Service",name,spec={"type":"ClusterIP","selector":({"component":name,"role":"server"} if name=="app" else {"component":name}),"ports":[{"port":port}]}))
            resources.append(self.object("PersistentVolumeClaim",name,spec={"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"1Gi"}}}))
        def selector(name):
            return {"matchLabels":{"component":name}}
        def tcp(port):
            return {"protocol":"TCP","port":port}
        dns = {"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"k8s-app":"kube-dns"}}}
        for name in ("app","db"):
            policy = {"podSelector":selector(name),"policyTypes":["Ingress","Egress"],"ingress":[],"egress":[]}
            if name=="app":
                policy["egress"]=[{"to":[{"podSelector":selector("db")}],"ports":[tcp(5432)]},
                                  {"to":[dns],"ports":[tcp(53),{"protocol":"UDP","port":53}]}]
            else:
                policy["ingress"]=[{"from":[{"podSelector":selector("app")}],"ports":[tcp(5432)]}]
            if name=="app" and self.cfg.get("web"):
                web=self.cfg["web"]
                peer={"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":web["ingressNamespace"]}},
                      "podSelector":{"matchLabels":web["ingressLabels"]}}
                policy["ingress"]=[{"from":[peer],"ports":[tcp(8080)]}]
            resources.append(self.object("NetworkPolicy",name,spec=policy))
        pg = {"name":"db","image":"postgres:17.12-alpine","args":["-c","ssl=on","-c","ssl_cert_file=/tls/db.crt","-c","ssl_key_file=/tls/db.key"],
              "envFrom":[{"secretRef":{"name":"pg-admin"}}],"volumeMounts":[{"name":k,"mountPath":v} for k,v in (("data","/data"),("tls","/tls"),("init","/docker-entrypoint-initdb.d"))],
              "readinessProbe":{"exec":{"command":["pg_isready","-h","127.0.0.1","-d","candidate"]}}}
        pod = {"automountServiceAccountToken":False,"serviceAccountName":"candidate","securityContext":{"fsGroup":70},
               "volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"db"}},{"name":"tls","secret":{"secretName":"pg-tls","defaultMode":0o440}},
                          {"name":"init","configMap":{"name":"pg-init"}}],"containers":[pg]}
        resources.append(self.deployment("db",pod))
        save("resources.json",resources)
        for obj in resources:
            self.apply(obj)
        command(*self.k,"rollout","status","deployment/db","--timeout=180s")

    def deployment(self,name,pod):
        labels={"component":name}
        return self.object("Deployment",name,spec={"replicas":1,"strategy":{"type":"Recreate"},"selector":{"matchLabels":labels},
                    "template":{"metadata":{"labels":labels},"spec":pod}})

    def workload_image(self,image):
        if self.cfg.get("delivery")=="node":
            require(re.fullmatch(r"sha256:[a-f0-9]{64}",image),"候选镜像内容摘要无效")
            ref=self.cfg["repository"]+":"+image.replace(":","-")
            require(command("docker","image","inspect","--format","{{.Id}}",ref)==image,"节点镜像内容已更换")
            return ref
        require(re.fullmatch(re.escape(self.cfg["repository"])+r"@sha256:[0-9a-f]{64}",image),"候选镜像摘要无效")
        return image

    def launch(self,image):
        self.owned()
        ref=self.workload_image(image)
        container={"name":"app","image":ref,"args":["migrate"],"envFrom":[{"secretRef":{"name":"migration"}}],
                   "securityContext":{"allowPrivilegeEscalation":False,"readOnlyRootFilesystem":True,"capabilities":{"drop":["ALL"]}},
                   "volumeMounts":[{"name":"ca","mountPath":"/ca","readOnly":True}]}
        if self.cfg.get("delivery")=="node":
            container["imagePullPolicy"]="Never"
        pod={"serviceAccountName":"candidate","automountServiceAccountToken":False,"securityContext":{"runAsNonRoot":True,"fsGroup":10000},
             "containers":[container],"volumes":[{"name":"ca","configMap":{"name":"pg-ca"}}]}
        if self.cfg.get("delivery")=="node":
            pod["nodeSelector"]={"kubernetes.io/hostname":self.cfg["buildNode"]}
        job=self.object("Job","migration-"+secrets.token_hex(6),spec={"backoffLimit":0,"activeDeadlineSeconds":120,"ttlSecondsAfterFinished":300,
                   "template":{"metadata":{"labels":{"component":"app","role":"migration"}},"spec":{**pod,"restartPolicy":"Never"}}})
        command(*self.k,"create","-f","-",data=json.dumps(job))
        command(*self.k,"wait","--for=condition=complete","job/"+job["metadata"]["name"],"--timeout=135s")
        command(*self.k,"exec","deployment/db","--","psql","-U","postgres","-d","candidate","-v","ON_ERROR_STOP=1","-c",
                "REVOKE INSERT,UPDATE,DELETE ON omnirelay_schema_migrations FROM candidate_runtime")
        container.update(args=["serve"],envFrom=[{"secretRef":{"name":"runtime"}}],readinessProbe={"httpGet":{"path":"/readyz","port":8080}})
        pod["volumes"].append({"name":"files","persistentVolumeClaim":{"claimName":"app"}})
        container["volumeMounts"].append({"name":"files","mountPath":"/data"})
        web=self.cfg.get("web")
        if web:
            bridge=PRIVATE/"web"
            bridge.mkdir(exist_ok=True,mode=0o700)
            command("setfacl","-m","u:10000:--x",str(bridge))
            pod["nodeSelector"]={"kubernetes.io/hostname":web["managerNode"]}
            pod["volumes"].append({"name":"bridge","hostPath":{"path":str(bridge),"type":"Directory"}})
            container["volumeMounts"].append({"name":"bridge","mountPath":"/run/omnirelay-candidate","readOnly":True})
            container["env"]=[{"name":name,"value":web[field]} for name,field in (
                ("OMNIRELAY_CANDIDATE_ORIGIN","origin"),("OMNIRELAY_FEISHU_APP_ID","appID"),
                ("OMNIRELAY_FEISHU_TENANT_KEY","tenantKey"),("OMNIRELAY_BOOTSTRAP_OPEN_ID","bootstrap"))]
            container["env"].append({"name":"OMNIRELAY_CANDIDATE_POD","valueFrom":{"fieldRef":{"fieldPath":"metadata.uid"}}})
            host=web["origin"].removeprefix("https://")
            ingress=self.object("Ingress","candidate",spec={"ingressClassName":web["ingressClass"],
                "tls":[{"hosts":[host],"secretName":web["tlsSecret"]}],
                "rules":[{"host":host,"http":{"paths":[{"path":"/","pathType":"Prefix",
                    "backend":{"service":{"name":"app","port":{"number":8080}}}}]}}]})
            ingress["metadata"]["annotations"]={"nginx.ingress.kubernetes.io/enable-access-log":"false",
                "nginx.ingress.kubernetes.io/ssl-redirect":"true"}
            save("ingress.json",ingress)
            self.apply(ingress)
        obj=self.deployment("app",pod)
        # Job 和常驻 Pod 共用网络策略，但 Service 仅选择常驻 Pod。
        obj["spec"]["template"]["metadata"]["labels"]["role"]="server"
        self.apply(self.object("Service","app",spec={"type":"ClusterIP","selector":{"component":"app","role":"server"},"ports":[{"port":8080}]}))
        save("deployment.json",obj)
        self.apply(obj)
        command(*self.k,"rollout","status","deployment/app","--timeout=180s")

    def verify(self,image):
        self.owned()
        pods=self.get("pods","-l","role=server")["items"]
        require(len(pods)==1,"需要唯一候选实例")
        pod=pods[0]
        spec=pod["spec"]
        state=pod["status"]["containerStatuses"]
        require(len(state)==1 and state[0]["ready"] and state[0]["imageID"].removeprefix("docker://").split("@")[-1]==image.split("@")[-1]
                and spec["containers"][0]["image"]==self.workload_image(image) and spec["serviceAccountName"]=="candidate"
                and spec["automountServiceAccountToken"] is False,"运行镜像或服务身份不匹配")
        services=self.get("service")["items"]
        require({s["metadata"]["name"] for s in services}=={"app","db"} and all(
            s["spec"]["type"]=="ClusterIP" and not s["spec"].get("externalIPs") for s in services),"候选服务存在外部入口")
        routes=self.get("ingress")["items"]
        if self.cfg.get("web"):
            expected=json.loads((PRIVATE/"ingress.json").read_text())
            require(len(routes)==1 and routes[0]["metadata"]["name"]=="candidate" and routes[0]["spec"]==expected["spec"]
                    and all(routes[0]["metadata"].get("annotations",{}).get(k)==v for k,v in expected["metadata"]["annotations"].items()),
                    "候选固定 HTTPS 路由不匹配")
        else:
            require(not routes,"候选存在外部路由")
        policies=self.get("networkpolicy")["items"]
        expected={p["metadata"]["name"]:p["spec"] for p in json.loads((PRIVATE/"resources.json").read_text()) if p["kind"]=="NetworkPolicy"}
        actual={p["metadata"]["name"]:p["spec"] for p in policies}
        for policy in actual.values():
            policy.setdefault("egress",[])
            policy.setdefault("ingress",[])
        require(actual==expected,"候选网络边界发生变化")
        # 在实际运行 Pod 中验证数据面。先确认候选库可达，生产探测均使用
        # 已就绪 Endpoint 的数字地址，避免把 DNS 失败误判为隔离成功。
        prod=["kubectl","--context",self.cfg["context"],"-n",self.cfg["productionNamespace"]]
        endpoints=json.loads(command(*prod,"get","endpointslices","-o","json"))["items"]
        pod_ips={item["ip"] for item in pod["status"]["podIPs"]}
        require(not any(pod_ips.intersection(e["addresses"]) for group in endpoints for e in group["endpoints"]),
                "生产服务发现中包含候选地址")
        denied=[]
        for name in ("productionDatabase","productionObjects"):
            binding=self.cfg[name]
            targets=[a for group in endpoints if group["metadata"]["labels"].get("kubernetes.io/service-name")==binding["service"]
                     and any(port.get("port")==binding["port"] for port in group["ports"])
                     for e in group["endpoints"] if e.get("conditions",{}).get("ready") is True for a in e["addresses"]]
            require(targets,"生产对照端点未就绪")
            denied.extend((address,binding["port"]) for address in targets)
            svc=json.loads(command(*prod,"get","service",binding["service"],"-o","json"))["spec"]
            for port in svc["ports"]:
                for ip in svc.get("clusterIPs",[]):
                    if ip!="None":
                        denied.append((ip,port["port"]))
        probe="""import json,socket,sys
socket.create_connection(('db',5432),2).close()
for address in json.load(sys.stdin):
    try: conn=socket.create_connection(tuple(address),2)
    except OSError: continue
    conn.close()
    sys.exit(1)
"""
        command(*self.k,"exec","-i","deployment/app","--","python3","-c",probe,data=json.dumps(denied))
        for verb in ("get","list","watch","create","update","patch","delete","deletecollection"):
            answer=subprocess.run(["kubectl","--context",self.cfg["context"],"auth","can-i",verb,"leases.coordination.k8s.io"+("/"+self.cfg["productionLease"] if verb in ("get","update","patch","delete") else ""),
                        "-n",self.cfg["productionNamespace"],"--as=system:serviceaccount:"+self.ns+":candidate",
                        "--as-group=system:serviceaccounts","--as-group=system:serviceaccounts:"+self.ns,"--as-group=system:authenticated"],capture_output=True,text=True)
            require(answer.returncode==1 and answer.stdout.strip()=="no","无法证明生产 Lease 操作被拒绝")
        return pod["metadata"]["uid"]
