# Phone video ingest design

## Goal

Phone photos and videos should sync reliably to the desktop, be retained in an archive, and be handed to DStore without DStore deletions propagating back to the phone.

## Folder layout

```text
/media/peter/storage/PEER_TO_PEER/
├── phone-inbox/              # Syncthing destination; never watched by DStore
├── phone-archive/            # Permanent desktop copy
└── outfiles/phone/           # DStore input; not shared with Syncthing
```

The phone syncs `DCIM/Camera` to `phone-inbox`. DStore watches `outfiles` recursively, so files copied into `outfiles/phone` are processed normally.

## Syncthing settings

Desktop folder:

- Folder ID: `phone-inbox`
- Path: `/media/peter/storage/PEER_TO_PEER/phone-inbox`
- Shared with: `peter-android`
- Folder type: Receive Only
- Ignore Deletes: enabled

Phone folder:

- Folder ID: `phone-inbox`
- Local path: `Internal storage/DCIM/Camera`
- Shared with: `peter-All-Series`
- Folder type: Send Only

The folder IDs must match exactly. The desktop Syncthing folder must not point inside `outfiles`.

## Processing workflow

1. Syncthing transfers a camera file into `phone-inbox`.
2. Wait until Syncthing reports the file fully synced and no temporary file remains.
3. Copy the completed file to `phone-archive`.
4. Copy the same completed file to `outfiles/phone`.
5. DStore detects it recursively and creates its manifest/shards.
6. The phone original can be deleted only after the archive copy and DStore handoff are verified.

The archive and DStore folders are outside the Syncthing folder. Deleting a phone original therefore cannot delete either desktop copy.

## Migration from the current setup

1. Stop or pause the current `phone-videos` folder.
2. Create `phone-inbox` and `phone-archive`.
3. Copy any files currently in `outfiles/phone` that must be retained into `phone-archive`.
4. Change the desktop Syncthing folder path to `phone-inbox`, or remove and recreate it with folder ID `phone-inbox`.
5. Set the desktop folder to Receive Only and enable Ignore Deletes.
6. Change the phone folder ID to `phone-inbox` and confirm its path remains `DCIM/Camera`.
7. Confirm a small test file arrives in `phone-inbox` before resuming normal use.

Do not delete the existing `outfiles/phone` contents during migration. DStore may already have converted some of them into `.dstore` stubs.

## Capacity and failure behavior

The archive is a second full desktop copy, so it requires additional disk space. DStore then creates its normal 5+2 shards. A full peer or failed shard upload leaves the source in `outfiles/phone` for retry; it does not silently claim the file is backed up.
