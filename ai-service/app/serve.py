"""API launcher with a psycopg-compatible Windows event loop."""
import asyncio
import os
import sys

import uvicorn


def main():
    host=os.environ.get("AI_HTTP_HOST","127.0.0.1")
    port=int(os.environ.get("AI_HTTP_PORT","8000"))
    if not 1<=port<=65535:
        raise SystemExit("invalid AI_HTTP_PORT")
    server=uvicorn.Server(uvicorn.Config("app.main:create_app",factory=True,host=host,port=port,access_log=False))
    with asyncio.Runner(loop_factory=asyncio.SelectorEventLoop if sys.platform=="win32" else asyncio.new_event_loop) as runner:
        runner.run(server.serve())


if __name__=="__main__":
    main()
