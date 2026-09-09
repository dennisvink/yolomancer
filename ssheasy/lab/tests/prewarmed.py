"""Run inside the workspace image as participant, with --network none."""
import os,pathlib,subprocess,time

assert os.getuid()!=0
for name in ['GOCACHE','GOMODCACHE']:
    cache=pathlib.Path(os.environ[name])
    assert cache.is_dir() and os.access(cache,os.W_OK),name
assert pathlib.Path('/workspace/.git').is_dir()
assert pathlib.Path('/workspace/yolomancer').stat().st_mode & 0o777 == 0o755
for path in ['/home/participant/.aws/credentials','/home/participant/.yolomancer/config.toml','/home/participant/.ssh/id_ed25519']:
    assert not pathlib.Path(path).exists(),path
env=dict(os.environ,GOMAXPROCS='2',GOPROXY='off',GOSUMDB='off')
started=time.monotonic()
subprocess.run(['go','mod','download'],cwd='/workspace',env=env,check=True,timeout=60)
subprocess.run(['go','build','-p','2','-o','/workspace/yolomancer','.'],cwd='/workspace',env=env,check=True,timeout=120)
print(f'PASS offline cached dependency check and rebuild: {time.monotonic()-started:.2f}s',flush=True)
print('PASS participant-owned caches, executable, public source, and no participant credentials')
