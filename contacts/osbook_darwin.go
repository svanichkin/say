//go:build darwin && cgo

package contacts

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Contacts -framework Foundation
#import <Contacts/Contacts.h>
#include <stdlib.h>
#include <string.h>

// sayContactsStatus is the access as it is, asking nothing: 0 not asked
// yet, 1 granted, 2 denied.
static int sayContactsStatus(void) {
	CNAuthorizationStatus st = [CNContactStore authorizationStatusForEntityType:CNEntityTypeContacts];
	if (st == CNAuthorizationStatusAuthorized) return 1;
	if (st == CNAuthorizationStatusNotDetermined) return 0;
	return 2;
}

// sayContactsAccess asks for the address book (the system asks the user
// the first time; Say waits up to two minutes): 1 granted, 0 not.
static int sayContactsAccess(void) {
	CNAuthorizationStatus st = [CNContactStore authorizationStatusForEntityType:CNEntityTypeContacts];
	if (st == CNAuthorizationStatusAuthorized) return 1;
	if (st != CNAuthorizationStatusNotDetermined) return 0;
	__block BOOL ok = NO;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	CNContactStore *store = [CNContactStore new];
	[store requestAccessForEntityType:CNEntityTypeContacts completionHandler:^(BOOL granted, NSError *e) {
		ok = granted;
		dispatch_semaphore_signal(sem);
	}];
	dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 120 * NSEC_PER_SEC));
	return ok ? 1 : 0;
}

static char *sayJSON(id obj) {
	NSData *d = [NSJSONSerialization dataWithJSONObject:obj options:0 error:nil];
	if (d == nil) return strdup("{}");
	char *s = malloc(d.length + 1);
	memcpy(s, d.bytes, d.length);
	s[d.length] = 0;
	return s;
}

// sayContactsRead is every card: names and instant messaging addresses,
// and which card is ours, as JSON.
static char *sayContactsRead(void) {
	@autoreleasepool {
		CFAbsoluteTime t0 = CFAbsoluteTimeGetCurrent(); // the stages' times, for the log
		CNContactStore *store = [CNContactStore new];
		CFAbsoluteTime tStore = CFAbsoluteTimeGetCurrent();
		NSArray *keys = @[CNContactIdentifierKey, CNContactGivenNameKey, CNContactFamilyNameKey,
			CNContactMiddleNameKey, CNContactNicknameKey, CNContactOrganizationNameKey,
			CNContactInstantMessageAddressesKey, CNContactUrlAddressesKey];
		NSError *err = nil;
		CFAbsoluteTime tMe = CFAbsoluteTimeGetCurrent(); // our card is asked aside (sayContactsMe): it takes seconds
		NSMutableArray *people = [NSMutableArray array];
		CNContactFetchRequest *req = [[CNContactFetchRequest alloc] initWithKeysToFetch:keys];
		[store enumerateContactsWithFetchRequest:req error:&err usingBlock:^(CNContact *c, BOOL *stop) {
			NSMutableArray *ims = [NSMutableArray array];
			for (CNLabeledValue<CNInstantMessageAddress *> *lv in c.instantMessageAddresses) {
				NSString *label = lv.label ? [CNLabeledValue localizedStringForLabel:lv.label] : @"";
				[ims addObject:@{@"label": label ?: @"", @"service": lv.value.service ?: @"", @"user": lv.value.username ?: @""}];
			}
			NSMutableArray *urls = [NSMutableArray array];
			for (CNLabeledValue<NSString *> *lv in c.urlAddresses) {
				if (![[lv.value lowercaseString] hasPrefix:@"say:"]) continue;
				NSString *label = lv.label ? [CNLabeledValue localizedStringForLabel:lv.label] : @"";
				[urls addObject:@{@"label": label ?: @"", @"url": lv.value ?: @""}];
			}
			NSString *name = c.organizationName ?: @""; // the name is made of the parts (Go)
			[people addObject:@{@"id": c.identifier, @"given": c.givenName ?: @"", @"family": c.familyName ?: @"",
				@"middle": c.middleName ?: @"", @"nick": c.nickname ?: @"", @"name": name ?: @"", @"im": ims, @"url": urls}];
		}];
		CFAbsoluteTime tEnum = CFAbsoluteTimeGetCurrent();
		// The order names are shown in (Contacts' setting): asked once.
		CNMutableContact *probe = [CNMutableContact new];
		probe.givenName = @"A";
		probe.familyName = @"B";
		BOOL familyFirst = [CNContactFormatter nameOrderForContact:probe] == CNContactDisplayNameOrderFamilyNameFirst;
		CFAbsoluteTime tEnd = CFAbsoluteTimeGetCurrent();
		NSString *timing = [NSString stringWithFormat:@"store %.0f ms, cards %.0f ms, order %.0f ms",
			(tStore - t0) * 1000, (tEnum - tMe) * 1000, (tEnd - tEnum) * 1000];
		return sayJSON(@{@"people": people, @"family_first": @(familyFirst), @"timing": timing,
			@"error": err ? err.localizedDescription : @""});
	}
}

// sayContactsMe is the id of the card the system has as ours ("My Card";
// "" for none). Contacts takes seconds for it.
static char *sayContactsMe(void) {
	@autoreleasepool {
		CNContactStore *store = [CNContactStore new];
		CNContact *me = [store unifiedMeContactWithKeysToFetch:@[CNContactIdentifierKey] error:nil];
		return strdup(me != nil ? me.identifier.UTF8String : "");
	}
}

// sayContactsChanged is Go's: the system's book changed.
extern void sayContactsChanged(void);

// sayContactsWatch follows the system's book: Contacts posts a note when a
// card changes, by any program (Say's own writes too).
static void *sayContactsWatch(void) {
	CNContactStore *store = [CNContactStore new]; // kept: the notes come while one lives
	id token = [[NSNotificationCenter defaultCenter] addObserverForName:CNContactStoreDidChangeNotification
		object:nil queue:[NSOperationQueue new] usingBlock:^(NSNotification *n) {
			sayContactsChanged();
		}];
	return (__bridge_retained void *)@[store, token];
}

static void sayContactsUnwatch(void *w) {
	NSArray *a = (__bridge_transfer NSArray *)w;
	[[NSNotificationCenter defaultCenter] removeObserver:a[1]];
}

// sayContactsWrite writes a card's names and its Say addresses, links
// under their labels (the other links and addresses kept, Say's instant
// messaging addresses of before removed); a card without an id is added.
// JSON in and out.
static char *sayContactsWrite(const char *in) {
	@autoreleasepool {
		NSData *d = [NSData dataWithBytes:in length:strlen(in)];
		NSDictionary *p = [NSJSONSerialization JSONObjectWithData:d options:0 error:nil];
		if (![p isKindOfClass:[NSDictionary class]]) return sayJSON(@{@"error": @"bad input"});
		CNContactStore *store = [CNContactStore new];
		NSError *err = nil;
		NSString *ident = p[@"id"];
		CNMutableContact *m = nil;
		if (ident.length > 0) {
			NSArray *keys = @[CNContactGivenNameKey, CNContactFamilyNameKey, CNContactMiddleNameKey,
				CNContactNicknameKey, CNContactInstantMessageAddressesKey, CNContactUrlAddressesKey];
			CNContact *c = [store unifiedContactWithIdentifier:ident keysToFetch:keys error:&err];
			if (c == nil) return sayJSON(@{@"error": err ? err.localizedDescription : @"no such card"});
			m = [c mutableCopy];
		} else {
			m = [CNMutableContact new];
		}
		m.givenName = p[@"given"] ?: @"";
		m.familyName = p[@"family"] ?: @"";
		m.middleName = p[@"middle"] ?: @"";
		m.nickname = p[@"nick"] ?: @"";
		NSMutableArray *ims = [NSMutableArray array];
		for (CNLabeledValue<CNInstantMessageAddress *> *lv in m.instantMessageAddresses) {
			BOOL say = [lv.value.service caseInsensitiveCompare:@"Say"] == NSOrderedSame ||
				[[lv.value.username lowercaseString] hasPrefix:@"say:"];
			if (!say) [ims addObject:lv];
		}
		m.instantMessageAddresses = ims; // Say's of before go: its links replace them
		NSMutableArray *urls = [NSMutableArray array];
		for (CNLabeledValue<NSString *> *lv in m.urlAddresses) {
			if (![[lv.value lowercaseString] hasPrefix:@"say:"]) [urls addObject:lv];
		}
		NSArray *say = p[@"url"];
		if ([say isKindOfClass:[NSArray class]]) {
			for (NSDictionary *u in say) {
				NSString *label = u[@"label"];
				[urls addObject:[CNLabeledValue labeledValueWithLabel:(label.length ? label : nil) value:(u[@"url"] ?: @"")]];
			}
		}
		m.urlAddresses = urls;
		CNSaveRequest *req = [CNSaveRequest new];
		if (ident.length > 0) {
			[req updateContact:m];
		} else {
			[req addContact:m toContainerWithIdentifier:nil];
		}
		if (![store executeSaveRequest:req error:&err]) {
			return sayJSON(@{@"error": err ? err.localizedDescription : @"not saved"});
		}
		return sayJSON(@{@"id": m.identifier ?: @""});
	}
}
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// accessTook is how long the last access check took (the log).
var accessTook time.Duration

// OSBookAvailable reports whether there is a system address book Say can
// use here.
func OSBookAvailable() bool { return true }

// granted: the system let Say use the address book (asked once a run: the
// check takes a tenth of a second).
var granted atomic.Bool

// OSBookStatus is the access to the system's book as it is (asking
// nothing): NotAsked, Granted or Denied.
func OSBookStatus() int {
	if granted.Load() {
		return Granted
	}
	return int(C.sayContactsStatus())
}

// OSBookAccess asks the system for the address book (the first time it
// asks the user) and reports whether Say may use it.
func OSBookAccess() bool {
	if granted.Load() {
		return true
	}
	ok := C.sayContactsAccess() == 1
	granted.Store(ok)
	return ok
}

func readOS() ([]byte, error) {
	start := time.Now()
	if !OSBookAccess() {
		return nil, ErrNoAccess
	}
	accessTook = time.Since(start)
	s := C.sayContactsRead()
	defer C.free(unsafe.Pointer(s))
	return []byte(C.GoString(s)), nil
}

var systemChanged struct {
	sync.Mutex
	f func()
}

//export sayContactsChanged
func sayContactsChanged() {
	systemChanged.Lock()
	f := systemChanged.f
	systemChanged.Unlock()
	if f != nil {
		// Aside, never waiting here: Contacts posts its note and waits for
		// the observers, also while our own write (under the store's lock)
		// is in progress; waiting for that lock here hung Say.
		go f()
	}
}

// watchSystem tells changed whenever the system's book changes.
func watchSystem(changed func()) (stop func()) {
	systemChanged.Lock()
	systemChanged.f = changed
	systemChanged.Unlock()
	w := C.sayContactsWatch()
	return func() {
		C.sayContactsUnwatch(w)
		systemChanged.Lock()
		systemChanged.f = nil
		systemChanged.Unlock()
	}
}

// readOSMe is the id of the system's own card for us.
func readOSMe() (string, error) {
	if !OSBookAccess() {
		return "", ErrNoAccess
	}
	s := C.sayContactsMe()
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s), nil
}

func writeOS(in []byte) ([]byte, error) {
	if !OSBookAccess() {
		return nil, ErrNoAccess
	}
	cin := C.CString(string(in))
	defer C.free(unsafe.Pointer(cin))
	s := C.sayContactsWrite(cin)
	defer C.free(unsafe.Pointer(s))
	return []byte(C.GoString(s)), nil
}
