// Package testdata provides SHA-256 constants for synthetic test fixture files.
// Files are generated deterministically:
//
//   sample.MP4  (1024 bytes): byte[i] = (i*17+42) % 256
//   sample.LRF  (512 bytes):  byte[i] = (i*13+7)  % 256
//   sample.WAV  (256 bytes):  byte[i] = (i*11+3)  % 256
//   corrupt_sample.MP4 (1024 bytes): same as sample.MP4 but byte[0] ^= 0xFF
package testdata

const (
	// SHA256SampleMP4 is the SHA-256 of testdata/sample.MP4.
	SHA256SampleMP4 = "175187df68f05725444e5c0d0940d88cb7e64beeccfdd3903d3236a1f6e08c63"

	// SHA256SampleLRF is the SHA-256 of testdata/sample.LRF.
	SHA256SampleLRF = "479ad71598de182171230acbe3322cdac3b9bb9f70894a7cc3e7b526be46693b"

	// SHA256SampleWAV is the SHA-256 of testdata/sample.WAV.
	SHA256SampleWAV = "2b35dc1f61cfbd2137c290d0b4d78266a10a5c5038498fe2c1c1be1e45489304"

	// SHA256CorruptMP4 is the SHA-256 of testdata/corrupt_sample.MP4.
	SHA256CorruptMP4 = "f04dbed2585c5504444be7d611e47358c0ce0202c4399f6d567f19ff4a68910b"

	// SizeMP4 is the size in bytes of sample.MP4.
	SizeMP4 = 1024

	// SizeLRF is the size in bytes of sample.LRF.
	SizeLRF = 512

	// SizeWAV is the size in bytes of sample.WAV.
	SizeWAV = 256
)
