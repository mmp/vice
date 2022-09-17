// gcc uuid.c -lole32

#include <stdio.h>
#include <unknwn.h>

void main(int argc, wchar_t* argv[])
{
    GUID guid;
    wchar_t wzGuid[39] = { 0 };
    int count = (1 < argc) ? _wtoi(argv[1]) : 1;

    for (int i = 0; i < count; ++i) 
    {
        if (SUCCEEDED(CoCreateGuid(&guid)) && StringFromGUID2(&guid, wzGuid, sizeof(wzGuid) / sizeof(wzGuid[0])))
        {
            _putws(wzGuid);
        }
    }
}
