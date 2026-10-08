import asyncio, sys, os
outdir = sys.argv[2]
os.makedirs(outdir, exist_ok=True)
count = 0

async def handle(reader, writer):
    global count
    writer.write(b"220 sink ESMTP\r\n"); await writer.drain()
    data = None
    while True:
        line = await reader.readline()
        if not line:
            break
        if data is not None:
            if line.rstrip(b"\r\n") == b".":
                count += 1
                with open(os.path.join(outdir, f"msg{count}.eml"), "wb") as f:
                    f.write(b"".join(data))
                data = None
                writer.write(b"250 2.0.0 Ok: queued as SINK\r\n")
            else:
                data.append(line[1:] if line.startswith(b"..") else line)
            await writer.drain(); continue
        cmd = line.split(b" ", 1)[0].upper().rstrip(b"\r\n")
        if cmd in (b"EHLO", b"HELO"):
            writer.write(b"250 sink\r\n")
        elif cmd == b"DATA":
            data = []; writer.write(b"354 go\r\n")
        elif cmd == b"QUIT":
            writer.write(b"221 bye\r\n"); await writer.drain(); break
        else:
            writer.write(b"250 2.0.0 Ok\r\n")
        await writer.drain()
    writer.close()

async def main():
    srv = await asyncio.start_server(handle, "0.0.0.0", int(sys.argv[1]))
    async with srv:
        await srv.serve_forever()
asyncio.run(main())
