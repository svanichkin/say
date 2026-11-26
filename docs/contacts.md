# Contacts

[Documentation](README.md) › Contacts

Say keeps no contact list of its own. It reads and writes your system's
address book (macOS) or a folder of vCard files, so the same people show up
in Say and in your other apps, and can be synced like any other contacts.

## Where they come from

You choose in **Settings**: **OS address book** (macOS) or **Contacts
folder**. Until you choose, Say uses:

| System | Default |
| --- | --- |
| macOS | The address book (Contacts). The system asks for access the first time; refused, `~/Library/Application Support/say/contacts` |
| Windows | `%USERPROFILE%\Contacts` |
| Linux, BSD | `~/.local/share/contacts` (`$XDG_DATA_HOME/contacts`, where vdirsyncer and khard keep cards) |
| Plan 9 | `$home/lib/contacts` |

**Contacts folder → Choose a folder** walks the folders from the top;
entering one chooses it. Once a folder is chosen, its path opens the same
walk at it, so `<` goes up a level at a time. `-contacts <folder>` on the
command line chooses one too.

## The vCard folder

The folder holds `.vcf` files: versions 2.1, 3.0 and 4.0 are read, 3.0 is
written, as macOS Contacts writes it. A file may hold several cards. A
person's devices are their Say addresses, each a link with a label:

```
BEGIN:VCARD
VERSION:3.0
FN:Alice Ivanova
N:Ivanova;Alice;;;
item1.URL:say:[200:abcd::1]
item1.X-ABLabel:home
item2.URL:say:[201:1::2]:7001
item2.X-ABLabel:phone
UID:6F1E…
END:VCARD
```

The port is written only when it is not 7777. A card Say changes keeps every
line it does not know (photos, phones, notes). Older cards with
`IMPP;X-SERVICE-TYPE=Say` addresses are read and rewritten as links.

The folder can be kept in step with a CardDAV server by vdirsyncer, or synced
by iCloud Drive, Dropbox or Syncthing. Say notices changes made by other
programs (FSEvents or kqueue on macOS, inotify on Linux,
ReadDirectoryChangesW on Windows, the address book's own notices); on Plan 9
it rereads only after its own writes.

## Your card

Your card comes first in **Contacts**. It holds this device, labelled with its
kind (**Laptop** when it has a battery, else **Computer**) unless you name it
in **This device's label**. Say puts this device's address on your card and
takes it off any other.

- **In a folder**, your card is the file marked `me`: an extended attribute
  (`me` on macOS, `user.me` on Linux, the alternate data stream `file.vcf:me`
  on Windows) whose value is the card's UID. Renaming the file keeps the mark.
  If another program rewrites the file and loses the mark, Say puts it back on
  the one card with this device's address.
- **In the address book**, it is the card Contacts has as yours, unless you
  choose another in **My card**.
- **With a folder and no card of yours**, Say makes one when it first starts:
  the computer's name as its nickname, this device on it. Rename it like any
  card.

## Adding, editing and deleting

- `+` in **Contacts** or in **Edit contacts** makes a person: names, a
  nickname, and devices (`+` adds one). Nothing is written while the form is
  open. Leaving it makes the card, if it has a name and an address; otherwise
  the empty fields blink the first time.
- **Settings → (folder or address book) → Edit contacts** opens each card's
  form: the names, the devices (each opening to its label and address), `+`
  to add a device and `−` to delete the person. A field is saved as you leave
  it.
- A device is removed by erasing its address, or by its `−`. `−` asks twice:
  the first press turns it into **Delete forever**.
- Deleting a person deletes a card Say made. A card made elsewhere (the
  system's book, a folder shared with other programs) loses only its Say
  addresses. Your own card cannot be deleted.

## Who is online

Every 30 seconds while no call is on, Say asks each device in the contacts
(but this one) whether Say runs there. It asks at once when it starts and
when the contacts change. A device that answers within 5 seconds gets a green
dot, and so does its person; a device asked that did not answer has a red dot
and is not called. Asking takes no call and does not take the line. See
[How it works](network.md#presence).

## Cards sent in a call

Right after a call connects, each side sends its own card (the names, the
nickname, the devices with their labels and addresses; nothing else). Without
a card of your own, a card of this device alone is sent.

- A card from an address already in your contacts is that person's for sure:
  an Yggdrasil address comes from its owner's key. What it adds is written to
  their card; nothing is overwritten.
- A card from an unknown address waits under **Unconfirmed N** in
  **Contacts**: **Confirm** (a new contact), **Add to <name>** (a contact of
  the same name), or **Delete**. Pending cards are kept apart from your
  contacts, in `config-contacts-pending/`.

## Settings files

How names show, the source, the folder, who was called last and your card in
the address book (when you chose another than Contacts' own) are kept in
`config-contacts.json` beside the configuration. See [Command
line](options.md#files).
