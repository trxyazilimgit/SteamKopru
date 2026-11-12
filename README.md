# SteamKöprü

**Steam üzerinden arkadaşınla port paylaş.** Port yönlendirme, modem ayarı, VPN ya da sabit IP gerekmez.

SteamKöprü, bilgisayarındaki bir portu (ör. Minecraft sunucusu, Valheim dünyası, yerel bir web sunucusu) Steam'in P2P ağı üzerinden arkadaşının bilgisayarına taşır. Arkadaşın oyunda sadece `localhost:<port>` adresine bağlanır, gerisini Steam halleder.

## Özellikler

- **TCP, UDP ve TCP+UDP** port paylaşımı, port aralıkları (`2456-2458`) ve birden fazla port
- **Hazır oyun listesi:** Minecraft (Java / Bedrock), Terraria, Valheim, Factorio, Stardew Valley, Project Zomboid, Palworld, Don't Starve Together, Satisfactory, 7 Days to Die, Rust ve diğerleri
- **Steam arkadaş listesi:** SteamKöprü'yü açmış arkadaşlar yeşil görünür; istersen SteamID ile de bağlanabilirsin
- **Erişim kontrolü:** sadece arkadaşlar, her bağlantıda onay, kalıcı izin listesi, engelleme
- **Canlı bağlantı paneli:** ping, bağlantı yolu (doğrudan / röle), alınan ve gönderilen veri, bağlantıyı kesme
- **Sistem tepsisi** ve **masaüstü bildirimleri**

## Gereksinimler

- Windows 10 / 11 (64-bit)
- Çalışan ve giriş yapılmış bir **Steam** istemcisi (her iki tarafta da)

## Kullanım

### Paylaşan taraf

1. SteamKöprü'yü aç ve **Paylaşan benim** sekmesine gir.
2. Paylaşmak istediğin portları ekle ya da **Hazır oyun ekle...** listesinden oyununu seç.
3. **Paylaşımı başlat**'a bas.

### Bağlanan taraf

1. **Arkadaşa bağlan** sekmesinde arkadaşını seç (listede yoksa SteamID'sini yapıştır).
2. **Bağlan**'a bas. Arkadaşının onayı gerekebilir.
3. Oyunda, ekranda yazan adrese bağlan (ör. `localhost:25565`).

## Kaynaktan derleme

Gerekenler: [Go](https://go.dev/) 1.27+, [LLVM/Clang](https://llvm.org/) (Fyne için CGO), Python 3 ve Git Bash.

```sh
sh build.sh
```

Çıktı `SteamKopru.exe` olarak oluşur. Çalışması için aynı klasörde şu dosyalar bulunmalıdır:

- `steam_api64.dll` — [Steamworks SDK](https://partner.steamgames.com/downloads/list)'dan alınır
- `steam_appid.txt` — içinde `480` yazar (Valve'ın herkese açık test uygulaması)

Simgeyi değiştirdiysen önce `go run ./tools/icongen` çalıştır.

Testler:

```sh
go test ./...
```

## Geliştirici

**Yusuf Özbaş** — [trxyazilim.com](https://trxyazilim.com)

## Lisans ve kullanım koşulları

Telif hakkı © 2026 Yusuf Özbaş. Tüm hakları saklıdır, aşağıdaki izinler hariç.

**Serbest olanlar:**

- Kaynak kodu incelemek, kullanmak, değiştirmek ve geliştirmek
- Projeyi fork'layıp üzerine yeni özellikler eklemek
- Değişikliklerini pull request olarak bu projeye göndermek

**Paylaşım şartı:** Bu yazılımı ya da ondan türetilen herhangi bir çalışmayı (değiştirilmiş sürümler, fork'lar, kodun bir kısmı, derlenmiş dosyalar) **paylaşırken, dağıtırken veya yayınlarken** aşağıdaki kaynak bilgisini açıkça ve görünür şekilde vermek **zorunludur**:

> Orijinal proje: SteamKöprü — Yusuf Özbaş ([trxyazilim.com](https://trxyazilim.com))

Bu bilgi README'de veya eşdeğer bir belgede ve varsa uygulama içindeki "Geliştirici / Hakkında" bölümünde yer almalıdır; kaldırılamaz ve değiştirilemez.

**Yasak olan:** Geliştiricinin adı kaynak olarak belirtilmeden paylaşmak, yazılımı kendi eseriymiş gibi sunmak, telif ve geliştirici bilgilerini silmek veya değiştirmek.

Yazılım "olduğu gibi" sunulur; kullanımından doğabilecek hiçbir zarardan geliştirici sorumlu tutulamaz.

---

*SteamKöprü, Valve Corporation ile bağlantılı değildir. Steam, Valve Corporation'ın ticari markasıdır.*
