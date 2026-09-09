import json,os,pathlib,pwd,signal,subprocess,time,urllib.request

base=os.environ['LAB_URL']
auth={'id':os.environ.pop('LAB_ID'),'token':os.environ.pop('LAB_TOKEN')}
def request(route,extra=None):
    data=json.dumps(dict(auth,**(extra or {}))).encode()
    with urllib.request.urlopen(urllib.request.Request(base+route,data=data,headers={'Content-Type':'application/json'}),timeout=30) as response:return json.load(response)
def private(path,value,mode=0o600):
    target=pathlib.Path(path);target.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
    with os.fdopen(os.open(target,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600),'w') as file:file.write(value)
    os.chmod(target,mode);os.chown(target,uid,gid);os.chown(target.parent,uid,gid)

def progress(stage):
    # Reporting is best-effort and sends fixed stage names, never tool output.
    for attempt in range(2):
        try:
            request('/progress',{'stage':stage})
            return
        except Exception:
            if attempt==0:time.sleep(1)

uid,gid=pwd.getpwnam('participant').pw_uid,pwd.getpwnam('participant').pw_gid
try:
    config=request('/bootstrap')
    if config['expires']<=time.time():raise RuntimeError('expired')
    progress('identity')
    private('/home/participant/.ssh/authorized_keys',config['authorizedKey']+'\n')
    private('/home/participant/.ssh/id_ed25519',config['privatePem'])
    private('/home/participant/.ssh/id_ed25519.pub',config['authorizedKey']+'\n',0o644)
    private('/home/participant/.ssh/config','Host *\n  BatchMode yes\n  StrictHostKeyChecking accept-new\n')
    os.chmod('/home/participant/.ssh',0o700)
    private('/home/participant/.yolomancer/registration-identity.json',json.dumps(config['registration']))
    environment=dict(os.environ,HOME='/home/participant',USER='participant',GIT_TERMINAL_PROMPT='0',GOMAXPROCS='2')
    # Run as the participant; never log the code, environment, or CLI output.
    def demote():os.setgid(gid);os.setuid(uid)
    progress('clone')
    # The image contains a clean public clone plus participant-owned Go caches.
    # Fast-forward only: never discard local changes or silently use stale code
    # after a failed update. No shared GitHub credentials are needed.
    if pathlib.Path('/workspace/.git').is_dir():
        subprocess.run(['git','pull','--ff-only','origin','main'],cwd='/workspace',env=environment,preexec_fn=demote,capture_output=True,check=True,timeout=180)
    else:
        subprocess.run(['git','clone','--depth','1','https://github.com/dennisvink/yolomancer.git','/workspace'],env=environment,preexec_fn=demote,capture_output=True,check=True,timeout=180)
    print('Source updated; rebuilding with cached dependencies.',flush=True)
    progress('dependencies')
    subprocess.run(['go','mod','download'],cwd='/workspace',env=environment,preexec_fn=demote,capture_output=True,check=True,timeout=900)
    progress('build')
    target='.' if pathlib.Path('/workspace/main.go').exists() else './cmd/yolomancer'
    subprocess.run(['go','build','-p','2','-o','/workspace/yolomancer',target],cwd='/workspace',env=environment,preexec_fn=demote,capture_output=True,check=True,timeout=900)
    os.chmod('/workspace/yolomancer',0o755)
    os.symlink('/workspace/yolomancer','/usr/local/bin/yolomancer')
    os.symlink('/workspace/yolomancer','/usr/bin/yolomancer')
    progress('register')
    for attempt in range(4):
        result=subprocess.run(['/usr/local/bin/yolomancer','register',config['code']],env=environment,preexec_fn=demote,capture_output=True,timeout=90)
        if result.returncode==0:break
        time.sleep(2**attempt)
    else:raise RuntimeError('registration failed')
    # AWS CLI uses the same participant credentials; no shared task role exists.
    import tomllib
    with open('/home/participant/.yolomancer/config.toml','rb') as file:settings=tomllib.load(file)
    private('/home/participant/.aws/credentials','[default]\naws_access_key_id='+settings['aws_access_key_id']+'\naws_secret_access_key='+settings['aws_secret_access_key']+'\n')
    private('/home/participant/.aws/config','[default]\nregion='+settings['aws_region']+'\n')
    progress('ssh')
    subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f','/etc/ssh/ssh_host_ed25519_key'],check=True,capture_output=True)
    fingerprint=subprocess.check_output(['ssh-keygen','-E','md5','-lf','/etc/ssh/ssh_host_ed25519_key.pub'],text=True).split()[1].removeprefix('MD5:')
    sshd=subprocess.Popen(['/usr/sbin/sshd','-D','-e'])
    request('/ready',{'fingerprint':fingerprint})
    progress('ready')
    print('Workspace ready.',flush=True)
    def shutdown(*args):
        sshd.terminate()
        raise SystemExit(0)
    signal.signal(signal.SIGTERM,shutdown)
    while time.time()<config['expires'] and sshd.poll() is None:time.sleep(1)
    sshd.terminate()
except Exception as error:
    progress('failed')
    print('Workspace bootstrap failed: '+type(error).__name__,flush=True)
    raise SystemExit(1)
